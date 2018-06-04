package main

import (
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/watch"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/elbv2"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/kelseyhightower/envconfig"
)

//Specification has the necessary environment variables
type Specification struct {
	AWSRegion        string `default:"us-west-2"`
	TargetGroupArns  string `required:"true"`
	LabelFilterKey   string `default:""`
	LabelFilterValue string `default:""`
	Prometheus       bool   `default:"true"`
	Port             string `default:"8080"`
}

var s Specification

func main() {

	log.SetFlags(log.LstdFlags | log.LUTC)

	err := envconfig.Process("nlb_sync", &s)
	if err != nil {
		log.Panic("Could not read environment variables: " + err.Error())
	}

	go provideMetrics()

	// Setup for a graceful worker exit on container shutdown
	signalChan := make(chan os.Signal)
	signal.Notify(signalChan, syscall.SIGTERM, syscall.SIGINT)

	wg := &sync.WaitGroup{}
	wg.Add(1)

	go handleMessages(s, wg, signalChan)

	wg.Wait()
}

func handleMessages(spec Specification, wg *sync.WaitGroup, exit <-chan os.Signal) {
	targetarns := strings.Split(s.TargetGroupArns, ",")

	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Panic(err.Error())
	}

	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Panic(err.Error())
	}

	//this will by default look for the env vars AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY which should exist
	sess, err := session.NewSession(&aws.Config{
		Region: aws.String(s.AWSRegion)},
	)
	svc := elbv2.New(sess)

	log.Println("Reading node events...")
	watcher, err := clientset.CoreV1().Nodes().Watch(metav1.ListOptions{})
	channel := watcher.ResultChan()

	for {
		select {
		case <-exit:
			wg.Done()
			return
		case event := <-channel:
			currentNode := event.Object.(*v1.Node)

			//toss out modified events
			if event.Type == watch.Modified {
				continue
			}

			log.Printf("Processing node %s:%s with event %s \n", currentNode.Name, currentNode.Spec.ExternalID, event.Type)

			//filter out certain nodes if desired based on the node labels
			if s.LabelFilterKey != "" && s.LabelFilterValue != "" && currentNode.Labels[s.LabelFilterKey] != s.LabelFilterValue {
				log.Printf("Ignoring node due to label filter: %s with label %s:%s \n", currentNode.Name, s.LabelFilterKey, currentNode.Labels[s.LabelFilterKey])
				continue
			}

			for _, targetarn := range targetarns {
				switch event.Type {
				case watch.Added:
					result, err := AddTarget(svc, targetarn, currentNode.Spec.ExternalID)
					if err != nil {
						log.Printf("error encountered adding node %s to target arn %s \n", currentNode.Spec.ExternalID, targetarn)
					} else {
						log.Printf("results of %s being added as a target %s of %s \n", currentNode.Spec.ExternalID, targetarn, result.GoString())
					}
					break

				case watch.Deleted:
					result, err := DeregisterTarget(svc, targetarn, currentNode.Spec.ExternalID)
					if err != nil {
						log.Printf("error encountered removing node %s to target arn %s \n", currentNode.Spec.ExternalID, targetarn)
					} else {
						log.Printf("results of %s being removed as a target %s of %s \n", currentNode.Spec.ExternalID, targetarn, result.GoString())
					}
					break
				}
			}
		}
	}
}

func provideMetrics() {
	if s.Prometheus == true {
		//add prometheus metrics
		port := ":" + s.Port
		http.Handle("/metrics", promhttp.Handler())
		log.Fatal(http.ListenAndServe(port, nil))
	}
}

//AddTarget Will Register a new node into the target list of a target group
func AddTarget(svc *elbv2.ELBV2, targetArn string, nodeID string) (*elbv2.RegisterTargetsOutput, error) {
	params := &elbv2.RegisterTargetsInput{
		TargetGroupArn: aws.String(targetArn),
		Targets: []*elbv2.TargetDescription{
			{
				Id: aws.String(nodeID),
			},
		},
	}

	result, err := svc.RegisterTargets(params)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case elbv2.ErrCodeTargetGroupNotFoundException:
				log.Println(elbv2.ErrCodeTargetGroupNotFoundException, aerr.Error())
			case elbv2.ErrCodeTooManyTargetsException:
				log.Println(elbv2.ErrCodeTooManyTargetsException, aerr.Error())
			case elbv2.ErrCodeInvalidTargetException:
				log.Println(elbv2.ErrCodeInvalidTargetException, aerr.Error())
			case elbv2.ErrCodeTooManyRegistrationsForTargetIdException:
				log.Println(elbv2.ErrCodeTooManyRegistrationsForTargetIdException, aerr.Error())
			default:
				log.Println(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			log.Println(err.Error())
		}
	}

	return result, err
}

//DeregisterTarget Will Deregister a destroyed node from the target list of a target group
func DeregisterTarget(svc *elbv2.ELBV2, targetArn string, nodeID string) (*elbv2.DeregisterTargetsOutput, error) {
	params := &elbv2.DeregisterTargetsInput{
		//"arn:aws:elasticloadbalancing:us-west-2:350717092402:targetgroup/tg-nonprod-http/6cd4ff3ddb3a6000"
		TargetGroupArn: aws.String(targetArn),
		Targets: []*elbv2.TargetDescription{
			{
				Id: aws.String(nodeID),
			},
		},
	}

	result, err := svc.DeregisterTargets(params)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case elbv2.ErrCodeTargetGroupNotFoundException:
				log.Println(elbv2.ErrCodeTargetGroupNotFoundException, aerr.Error())
			case elbv2.ErrCodeInvalidTargetException:
				log.Println(elbv2.ErrCodeInvalidTargetException, aerr.Error())
			default:
				log.Println(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			log.Println(err.Error())
		}
	}

	return result, err
}
