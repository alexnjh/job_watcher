package main

import (
	"os"
  "time"
  "fmt"
  "syscall"
  "os/signal"
  "context"
  "k8s.io/client-go/tools/cache"
  "k8s.io/apimachinery/pkg/labels"

  kubeinformers "k8s.io/client-go/informers"
  log "github.com/sirupsen/logrus"
  corev1 "k8s.io/api/core/v1"
  batchv1 "k8s.io/api/batch/v1"
  metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  jsoniter "github.com/json-iterator/go"
  configparser "github.com/bigkevmcd/go-configparser"

)

const (
  DefaultConfigPath = "/go/src/app/config.cfg"
)

// Initialize json encoder
var json = jsoniter.ConfigCompatibleWithStandardLibrary

// main code path
func main() {

  // Get required values
  confDir := os.Getenv("CONFIG_DIR")

  var config *configparser.ConfigParser
  var err error

  if len(confDir) != 0 {
    config, err = getConfig(confDir)
  }else{
    config, err = getConfig(DefaultConfigPath)
  }

  var mqHost, mqPort, mqUser, mqPass, defaultQueue string
  if err != nil {

    log.Errorf(err.Error())

    mqHost = os.Getenv("MQ_HOST")
    mqPort = os.Getenv("MQ_PORT")
    mqUser = os.Getenv("MQ_USER")
    mqPass = os.Getenv("MQ_PASS")
    defaultQueue = os.Getenv("DEFAULT_QUEUE")

    if len(mqHost) == 0 ||
    len(mqPort) == 0 ||
    len(mqUser) == 0 ||
    len(mqPass) == 0 ||
    len(defaultQueue) == 0{
  	   log.Fatalf("Config not found, Environment variables missing")
    }


  }else{

    mqHost, err = config.Get("QueueService", "hostname")
    if err != nil {
      log.Fatalf(err.Error())
    }
    mqPort, err = config.Get("QueueService", "port")
    if err != nil {
      log.Fatalf(err.Error())
    }
    mqUser, err = config.Get("QueueService", "user")
    if err != nil {
      log.Fatalf(err.Error())
    }
    mqPass, err = config.Get("QueueService", "pass")
    if err != nil {
      log.Fatalf(err.Error())
    }
    defaultQueue, err = config.Get("DEFAULTS", "default_queue")
    if err != nil {
      log.Fatalf(err.Error())
    }
  }

	// get the Kubernetes client for communicating with the kubernetes API server
	client := getKubernetesClient()

  // Create the required informer and listers for kubernetes resources
  kubefactory := kubeinformers.NewSharedInformerFactory(client, time.Second*30)
  job_informer := kubefactory.Batch().V1().Jobs().Informer()
  pod_lister := kubefactory.Core().V1().Pods().Lister()

  // Attempt to connect to the rabbitMQ server
  comm, err := NewRabbitMQCommunication(fmt.Sprintf("amqp://%s:%s@%s:%s/",mqUser, mqPass, mqHost, mqPort))
  if err != nil {
    log.Fatalf(err.Error())
  }

  err = comm.QueueDeclare(defaultQueue)
  if err != nil {
    log.Fatalf(err.Error())
  }

  // Add a event handler to listen for new pods
	job_informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {

		},
		UpdateFunc: func(oldObj, newObj interface{}) {

      new := newObj.(*batchv1.Job)

      if new.Status.Succeeded == *new.Spec.Completions {

        // Get the pod resource with this namespace/name

        labelMap, err := metav1.LabelSelectorAsMap(new.Spec.Selector)

        if err != nil {
          log.Errorf(err.Error())
        }else{
        	podlist, err := pod_lister.Pods(new.Namespace).List(labels.SelectorFromSet(labelMap))
        	if err != nil {
        		log.Errorf(err.Error())
        	}else{

            in, err := time.Parse(time.RFC3339, new.Annotations["creationTime"])

            if err != nil {
              log.Errorf(err.Error())
              return
            }

            out := new.Status.CompletionTime.UTC()

            if err != nil {
              log.Errorf(err.Error())
              return
            }

            go SendExperimentalPayload(&comm,podlist[0],in,out,defaultQueue)

            // Delete the job
            deletePolicy := metav1.DeletePropagationBackground
            if err := client.BatchV1().Jobs(new.Namespace).Delete(context.TODO(), new.Name, metav1.DeleteOptions{
              PropagationPolicy: &deletePolicy,
            }); err != nil {
              panic(err)
            }
          }
        }

      }

		},
		DeleteFunc: func(obj interface{}) {
		},
	})

	// use a channel to synchronize the finalization for a graceful shutdown
	stopCh := make(chan struct{})
	defer close(stopCh)

  // Start the informers
  kubefactory.Start(stopCh)

	// use a channel to handle OS signals to terminate and gracefully shut
	// down processing
	sigTerm := make(chan os.Signal, 1)
	signal.Notify(sigTerm, syscall.SIGTERM)
	signal.Notify(sigTerm, syscall.SIGINT)
	<-sigTerm
}

func SendExperimentalPayload(
  comm Communication,
  pod *corev1.Pod,
  in time.Time,
  out time.Time,
  queueName string){

  respBytes, err := json.Marshal(ExperimentPayload{Type:"Completed_Jobs",InTime:in,OutTime:out,Pod:pod})
  if err != nil {
    log.Fatalf("%s", err)
  }

  err = comm.Send(respBytes,queueName)

  if err != nil{
    log.Errorf(err.Error())
  }

}

type ExperimentPayload struct {
  Type string
  InTime time.Time
  OutTime time.Time
  Pod *corev1.Pod
}
