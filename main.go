package main

import (
	"fmt"
	"strings"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/watch"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/elbv2"
	"github.com/kelseyhightower/envconfig"
)

//Specification has the necessary environment variables
type Specification struct {
	AWSRegion        string `default:"us-west-2"`
	LoadBalancerArns string `required:"true"`
}

var s Specification

func main() {
	err := envconfig.Process("node-nlb-sync", &s)
	if err != nil {
		panic("Could not read environment variables")
	}
	targetarns := strings.Split(",", s.LoadBalancerArns)

	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	fmt.Println(config)

	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	//this will by default look for the env vars AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY which should exist
	sess, err := session.NewSession(&aws.Config{
		Region: aws.String(s.AWSRegion)},
	)
	svc := elbv2.New(sess)

	watcher, err := clientset.CoreV1().Nodes().Watch(metav1.ListOptions{})
	channel := watcher.ResultChan()

	for {

		event := <-channel
		currentNode := event.Object.(*v1.Node)

		//process each target arn
		for _, targetarn := range targetarns {
			switch event.Type {
			case watch.Added:
				fmt.Printf("node added %s \n", currentNode.Spec.ExternalID)
				result, err := AddTarget(svc, targetarn, currentNode.Spec.ExternalID)
				if err != nil {
					fmt.Printf("error encountered adding node %s to target arn %s \n", currentNode.Spec.ExternalID, targetarn)
				} else {
					fmt.Printf("results of %s being added as a target %s of %x \n", currentNode.Spec.ExternalID, targetarn, result.GoString())
				}
				break

			case watch.Deleted:
				fmt.Printf("node deleted %s \n", currentNode.Spec.ExternalID)
				result, err := DeregisterTarget(svc, targetarn, currentNode.Spec.ExternalID)
				if err != nil {
					fmt.Printf("error encountered removing node %s to target arn %s \n", currentNode.Spec.ExternalID, targetarn)
				} else {
					fmt.Printf("results of %s being removed as a target %s of %x \n", currentNode.Spec.ExternalID, targetarn, result.GoString())
				}
				break
			}

		}
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
				fmt.Println(elbv2.ErrCodeTargetGroupNotFoundException, aerr.Error())
			case elbv2.ErrCodeTooManyTargetsException:
				fmt.Println(elbv2.ErrCodeTooManyTargetsException, aerr.Error())
			case elbv2.ErrCodeInvalidTargetException:
				fmt.Println(elbv2.ErrCodeInvalidTargetException, aerr.Error())
			case elbv2.ErrCodeTooManyRegistrationsForTargetIdException:
				fmt.Println(elbv2.ErrCodeTooManyRegistrationsForTargetIdException, aerr.Error())
			default:
				fmt.Println(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			fmt.Println(err.Error())
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
				fmt.Println(elbv2.ErrCodeTargetGroupNotFoundException, aerr.Error())
			case elbv2.ErrCodeInvalidTargetException:
				fmt.Println(elbv2.ErrCodeInvalidTargetException, aerr.Error())
			default:
				fmt.Println(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			fmt.Println(err.Error())
		}
	}

	return result, err
}
