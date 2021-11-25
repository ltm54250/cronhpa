package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"net/http"
	"time"

	autoscalingv1 "example.com/cronhpa/api/v1"
	"example.com/cronhpa/server"
	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type WebServer struct {
	client.Client
	cronManager *CronManager
	crdMap      map[string]autoscalingv1.CronHPAContent
}

func (ws *WebServer) serve() {
	r := mux.NewRouter()
	r.HandleFunc("/api.json", ws.handleJobsController)
	r.HandleFunc("/index.html", ws.handleIndexController)
	r.HandleFunc("/getcronhpas", ws.handleGetCRDSController)
	r.HandleFunc("/createcronhpa", ws.handleCreateCRDController)
	r.HandleFunc("/updatecronhpa", ws.handleUpdateCRDController)
	r.HandleFunc("/", ws.handleIndexController)
	http.Handle("/", r)

	srv := &http.Server{
		Handler: r,
		Addr:    ":8000",
		// Good practice: enforce timeouts for servers you create!
		WriteTimeout: 15 * time.Second,
		ReadTimeout:  15 * time.Second,
	}
	klog.Fatal(srv.ListenAndServe())
}

type data struct {
	Items []Item
}

type responseData struct {
	Status int         `json:"status"`
	Msg    string      `json:"msg"`
	Data   interface{} `json:"data"`
}

type Item struct {
	Id        string
	Name      string
	CronHPA   string
	Namespace string
	Pre       string
	Next      string
}

func (ws *WebServer) handleIndexController(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl, _ := template.New("index").Parse(server.Template)
	entries := ws.cronManager.cronExecutor.ListEntries()
	d := data{
		Items: make([]Item, 0),
	}
	for _, e := range entries {
		job, ok := e.Job.(CronJob)
		if !ok {
			klog.Warningf("Failed to parse cronjob %v to web console", e)
			continue
		}
		d.Items = append(d.Items, Item{
			Id:        job.ID(),
			Name:      job.Name(),
			CronHPA:   job.CronHPAMeta().Name,
			Namespace: job.CronHPAMeta().Namespace,
			Pre:       e.Prev.String(),
			Next:      e.Next.String(),
		})
	}
	tmpl.Execute(w, d)
}

func (ws *WebServer) handleJobsController(w http.ResponseWriter, r *http.Request) {
	b, err := json.Marshal(ws.cronManager.cronExecutor.ListEntries())
	if err != nil {
		w.Write([]byte(err.Error()))
		return
	}
	w.Write(b)
}

func (ws *WebServer) handleGetCRDSController(w http.ResponseWriter, r *http.Request) {
	response(0, "", ws.crdMap, w)
}

func (ws *WebServer) handleCreateCRDController(w http.ResponseWriter, r *http.Request) {
	status := 0
	msg := "create crd success"
	if r.Method == "POST" {
		if content, ok := getBody2Content(r.Body); ok {
			instance := &autoscalingv1.CronHorizontalPodAutoscaler{
				TypeMeta:   metav1.TypeMeta{Kind: "CronHorizontalPodAutoscaler", APIVersion: "autoscaling.example.com/v1"},
				ObjectMeta: metav1.ObjectMeta{Name: content.Name, Namespace: content.Namespace},
				Spec:       content.Spec,
			}
			fmt.Println("prepare create cronhpa")
			if err := ws.Create(context.TODO(), instance); err != nil {
				fmt.Println("create cronhpa failed")
				klog.Errorf("create crd %v failed", instance)
				status = 1500
				msg = "internal err,create crd  failed"
			}
			fmt.Println("create cronhpa ok")
		} else {
			klog.Errorf("create crd err,get bofy failed")
			status = 1500
			msg = "internal err,get body failed"
		}
	} else {
		status = 1401
		msg = "method is not supported"
	}
	response(status, msg, "", w)
}

func (ws *WebServer) handleUpdateCRDController(w http.ResponseWriter, r *http.Request) {
	status := 0
	msg := ""
	if r.Method == "PUT" {
		if content, ok := getBody2Content(r.Body); ok {
			instance := &autoscalingv1.CronHorizontalPodAutoscaler{}
			if err := ws.Get(context.TODO(), types.NamespacedName{Name: content.Name, Namespace: content.Namespace}, instance); err != nil {
				status = 1500
				msg = "interal err,update get failed"
				klog.Errorf("get crd %v failed", instance)
			} else {
				instance.Spec = content.Spec
				if err = ws.Update(context.TODO(), instance); err != nil {
					klog.Errorf("update crd %v failed", instance)
					status = 1500
					msg = "interal err,update failed"
				}
			}
		} else {
			klog.Errorf("update crd err,get bofy failed")
			status = 1500
			msg = "internal err,get body failed"
		}
	} else {
		status = 1401
		msg = "method is not supported"
	}
	response(status, msg, "", w)
}

func NewWebServer(client client.Client, c *CronManager, crdmap map[string]autoscalingv1.CronHPAContent) *WebServer {
	return &WebServer{
		Client:      client,
		cronManager: c,
		crdMap:      crdmap,
	}
}

func getBody2Content(rbody io.ReadCloser) (ret autoscalingv1.CronHPAContent, ok bool) {
	ok = true
	ret = autoscalingv1.CronHPAContent{}
	if body, err := ioutil.ReadAll(rbody); err != nil {
		ok = false
		klog.Errorf("load body failed")
	} else {
		if err = json.Unmarshal(body, &ret); err != nil {
			ok = false
			fmt.Println(err)
			klog.Errorf("translate body to Spec failed")
		}
	}
	fmt.Println(ret)
	return ret, ok
}

func response(status int, msg string, data interface{}, w http.ResponseWriter) {
	ret := responseData{
		Msg:    msg,
		Status: status,
		Data:   data,
	}
	b, err := json.Marshal(ret)
	if err != nil {
		w.Write([]byte(err.Error()))
		return
	}
	w.Write(b)
}
