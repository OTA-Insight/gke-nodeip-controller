/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"errors"
	"fmt"
	"nodeip-controller/pkg/ipmanager"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// NodeReconciler reconciles a Node object

type NodeReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	recorder           record.EventRecorder
	NodePoolIpManagers map[string]ipmanager.IpManager
}

const (
	zoneLabel     = "topology.kubernetes.io/zone"
	nodePoolLabel = "cloud.google.com/gke-nodepool"
)

//+kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch;
//+kubebuilder:rbac:groups=core,resources=nodes/status,verbs=get

// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *NodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var node corev1.Node
	if err := r.Get(ctx, req.NamespacedName, &node); err != nil {
		log.Error(err, "unable to fetch node")
		// we'll ignore not-found errors, since they can't be fixed by an immediate
		// requeue (we'll need to wait for a new notification), and we can get them
		// on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.V(5).Info("Reconcile")
	// Check if this a node to operate on
	nodeLabels := node.ObjectMeta.Labels
	nodepoolName := nodeLabels[nodePoolLabel]
	ipManager, ok := r.NodePoolIpManagers[nodepoolName]
	if !ok {
		return ctrl.Result{}, nil
	}

	// This checks a node status field which should contain the external IP address, but there is a few seconds delay vs the real world(GCP compute)
	// when has changed/removed before the field is updated
	nodeExternalAddress, err := getExternalIP(&node)
	if err != nil {
		log.Info("Could not find externalIP on this node, continuing assigning IP of allowed pool")
	} else {
		if ipManager.IsAllowedIpAddress(nodeExternalAddress) {
			log.V(5).Info("Allowed IP address assigned")
			return ctrl.Result{}, nil
		}
		log.Info("Found unallowed external IP address on the node")
	}

	nodeName := node.ObjectMeta.Name
	nodeZone := nodeLabels[zoneLabel]

	newIp, err := ipManager.AssignAllowedIpAddress(ctx, nodeName, nodeZone)
	if err != nil {
		r.recorder.Event(&node, corev1.EventTypeWarning, "Assign IP failed", err.Error())
		return ctrl.Result{}, err
	}
	r.recorder.Event(&node, corev1.EventTypeNormal, "IP assigned", fmt.Sprintf("Ip address %s %s assigned", newIp.Name, newIp.Address))

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.recorder = mgr.GetEventRecorderFor("Node")
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		Complete(r)
}

func getExternalIP(node *corev1.Node) (string, error) {
	addresses := node.Status.Addresses
	for _, a := range addresses {
		if a.Type == corev1.NodeExternalIP {
			return a.Address, nil
		}
	}
	return "", errors.New("could not find external IP")
}
