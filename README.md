# NLB and Kubernetes Node Synchronization

As of right now, Kubernetes 1.9x only supports an alpha version of NLB that really is only written for nginx. We don't use nginx as our proxy of choice due to [limits within it's ingress rules](https://github.com/kubernetes/ingress-nginx/issues/1360). We instead use Traefik which supports regex and many other things in its ingress rules. Unfortunately, it does not support [TCP health checks just yet](https://github.com/containous/traefik/issues/1657) which means we need to use a normal http health check. 

Additionally, we want the ability to use static ips with our NLB to satisfy the requirements of some of clients and so an NLB lends itself well to that.

This repository contains a simple program that will keep a hand created target groups in sync with the Kubernetes cluster as nodes go up and down. All this is doing is making it possible to use an NLB in the interim until Kubernetes natively supports an NLB with the option of specifying health check details and annotations for the desired EIPs (Elastic IPs).

## Setup

Before we an even begin to create an NLB we need to expose a port on the EC2 instances that have your ingress controller running.

### K8s service setup
First you will need to have a service to expose Traefik or any other load balancer you like on a NodePort. Consider the following example. Really the key is that the type is NodePort. A NodePort will just expose an ephemeral port of the EC2 instance that the NLB can route traffic to.

```
kind: Service
apiVersion: v1
metadata:
  annotations:
    prometheus.io/scrape: "true"
  name: traefik-ingress-service-nlb
  namespace: ingress-traefik
spec:
  selector:
    k8s-app: traefik-ingress-lb
  ports:
    - protocol: TCP
      port: 80
      name: http
    - protocol: TCP
      port: 443
      name: https
  type: NodePort
  ```

  Once that has been setup you can run `kubectl get service` with your service name and namespace and look at the PORT(S) column. It'll look something like **80:25672/TCP,443:53821/TCP**. Take a note of the ports as you will need that to setup your NLB and TargetGroups (e.g. 25672 and 53821).

### Security Group setup
Now that we have a port exposed we need to allow that port through the security group. One of the cool things about an NLB is that it maintains the source IP address of the sender and so effectively any IP should be allowed to send to this port. 

Simply create Custom TCP rules for the Node Ports exposed earlier and allow all addresses to them (e.g. 0.0.0.0/0 for the Source address).

### NLB setup
Now that we have our ports configured and ready to go we can create the NLB and its target groups.

Steps to create an NLB (web ui):

* Navigate to the EC2 service group in web ui
* Select load balancers
* Click create load balancer
* Click create under the Network Load Balancer section
* Give it a name
* Leave single listener (typically port 80)
* Select the same vpc as your kubernetes cluster
* Select the AZs you want to use (recommended all) and select EIPs if you previously created them.
* Click next configure routing
* Name the new target group
* Leave the port as TCP
* Add the Port to be one of the ports that was exposed as a NodePort with the k8s service (e.g. 25672)
* Configure the health check to match your ingress controller. In the case of Traefik the /ping endpoint can be exposed but only responds to http.
* Under advanced health check settings, select override on the port and select the same port that matches the NodePort. Not necessary for http but will be for https.
* Click next register targets
* Leave this empty becaues the sync program in this repo will fill that in later automatically 
* Click Next Review
* Click Create

Now that the NLB has been created we need to make an adjustment for the https listener. We will go create a target group first:

* Select Target Groups from the navigation on the left of the screen
* Click Create Target Group
* Give the group a name
* Select TCP for protocol
* Fill in the NodePort that is meant to handle SSL traffic (e.g. 53821)
* Make sure the VPC selected matches your k8s cluster
* Change the health check settings to be the same as we created for the other target group for http above
* **NOTE** Again, make sure the health check port is overridden and matches what was used for http above otherwise your health checks will fail
* Click create target group

Now that we have a good target group for https we can add a listener to the NLB

* Select Load Balancers from the lefthand navigation
* Select the NLB previously created
* Select the Listeners tab
* Click add listener
* Select TCP with port 443
* Add a default action to forward to the https target group you newly created for https
* Click save

***The steps above assume your ingress controller is setup to decrypt your ssl traffic properly***

### IAM user

Before we can have all the necessary items to run this sync service successfully an IAM user will be necessary to give rights to the application to update your target groups. Here is an example inline security policy that can be associated with your IAM user. 

```
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Sid": "VisualEditor0",
            "Effect": "Allow",
            "Action": [
                "elasticloadbalancing:RegisterTargets",
                "elasticloadbalancing:DeregisterTargets"
            ],
            "Resource": [
                "<insert_http_targetgroup_arn>",
                "<insert_https_targetgroup_arn>"
            ]
        }
    ]
}
```
Make sure you have saved the credentials for programmatic access for this user for use later.


## Target Group Sync setup

Now that the NLB has been created, the target groups are ready, and we have an IAM user with correct rights we can finally implement the node-nlb-sync application to automatically add the instances to the target groups as the cluster scales up and down.

This assumes you will be deploying the program inside your k8s cluster using RBAC, feel free to make adjustments as necessary for your environment.

First, create a k8s secret with your IAM credentials and the target arns separated by commas. These will be provided to the servie as environment variables. I recommend you use [sealed secrets by bitnami](https://github.com/bitnami-labs/sealed-secrets) if you are pushing your yaml files into source control. If you need details on how to create a secret in k8s see the [Kubernetes docs](https://kubernetes.io/docs/concepts/configuration/secret/).

For RBAC you will need something like the following to get a service account that has rights to read node information on Kubernetes which can be used in your deployment.

```
---
kind: ClusterRole
apiVersion: rbac.authorization.k8s.io/v1beta1
metadata:
  name: nlb-sync-role
rules:
  - apiGroups:
      - ""
    resources:
      - nodes
    verbs:
      - get
      - list
      - watch
---
kind: ClusterRoleBinding
apiVersion: rbac.authorization.k8s.io/v1beta1
metadata:
  name: nlb-sync-role-binding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: nlb-sync-role
subjects:
- kind: ServiceAccount
  name: nlb-sync-sa
  namespace: ingress-traefik
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: nlb-sync-sa
  namespace: ingress-traefik
```

Now that we have that all set up you can finally configure the deployment.

```
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: nlb-sync
  namespace: ingress-traefik
  labels:
    app: nlb-sync
spec:
  replicas: 1
  template:
    metadata:
      labels:
        app: nlb-sync
    spec:
      serviceAccountName: nlb-sync-sa
      tolerations:
      - key: "process"
        value: "critical"
      nodeSelector:
        kops.k8s.io/instancegroup: critical
      containers:
        - image: ilprovo/node-nlb-sync:v0.16
          name: nlb-sync
          env:
            - name: AWS_ACCESS_KEY
              valueFrom: 
                secretKeyRef:
                  name: nlb-secret
                  key: aws_access_key
            - name: AWS_SECRET_KEY
              valueFrom: 
                secretKeyRef:
                  name: nlb-secret
                  key: aws_secret_key
            - name: NLB_SYNC_TARGETGROUPARNS
              valueFrom: 
                secretKeyRef:
                  name: nlb-secret
                  key: nlb_sync_targetgroup_arns
            - name: NLB_SYNC_LABELFILTERKEY
              value: kops.k8s.io/instancegroup
            - name: NLB_SYNC_LABELFILTERVALUE
              value: nodes
          resources:
            # keep request = limit to keep this container in guaranteed class
            limits:
              cpu: 500m
              memory: 400Mi
            requests:
              cpu: 250m
              memory: 200Mi
```

Notice that the environment variables AWS_ACCESS_KEY, AWS_SECRET_KEY, NLB_SYNC_TARGETGROUPARNS must be set in order for this to function correctly.

In addition, the application takes a few label filter variables so we can ignore nodes that we don't want to be part of our target groups (e.g. ignore the master nodes). The image `ilprovo/node-nlb-sync:v0.16` can be used if you like or you can generate and push to your own image repo with the Dockerfile included in this repo.

Once that deployment has been added then you will get the target groups automatically populated by the nodes that exist in the Kubernetes cluster. One nice thing about the Kubernetes listener is that you get an Added event for every node that exists at startup time so you know you won't be missing nodes at the beginning.

With the deployment running you should see your target groups in AWS get populated and turn healthy.

If your targets are unhealthy it is likely that the security group wasn't updated with the correct nodeports or that the health checks were not set up correctly. You can review the steps above to see more details and verify the steps were followed correctly. 

One thing you can do during troubleshooting is to simply do a curl on the health endpoint (e.g. `curl -v <node_ip>:32422/ping`) to an actual node ip on one of the nodeports and that will confirm the security group works properly and that the NodePort is exposed correctly. If this works then you know that your issue is simply misconfiguration of the health endpoints in your target groups.