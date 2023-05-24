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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"golang.org/x/oauth2/google"
	"google.golang.org/api/compute/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"nodeip-controller/controllers"
	"nodeip-controller/pkg/ipmanager"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

const (
	GCP_PROJECT_ID_ENV    = "GCP_PROJECT_ID"
	GCP_REGION_ENV        = "GCP_REGION"
	GKE_NODEPOOL_NAME_ENV = "GKE_NODEPOOL_NAME"
	IP_ADDRESS_FILTER_ENV = "IP_ADDRESS_FILTER"
	refreshInterval       = 1 * time.Hour
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var region string
	var project string
	nodePoolIpFilterMapping := map[string]string{}
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.Func("nodepool-ip-filter", "GKE nodepool and ipfilter separated by `=`", func(s string) error {
		strs := strings.SplitN(s, "=", 2)
		nodePoolIpFilterMapping[strs[0]] = strs[1]
		return nil
	})
	flag.CommandLine.StringVar(&region, "gcp-region", "", "GCP region where IP addreses are defined")
	flag.CommandLine.StringVar(&project, "gcp-project", "", "GCP project where IP addreses are defined")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	fmt.Println(nodePoolIpFilterMapping)
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "3505fd37.my.domain",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// ipFilter := os.Getenv(IP_ADDRESS_FILTER_ENV)
	// nodePoolName := os.Getenv(GKE_NODEPOOL_NAME_ENV)

	c, err := google.DefaultClient(context.Background(), compute.CloudPlatformScope)
	if err != nil {
		setupLog.Error(err, "Failed to get credential")
		os.Exit(1)
	}

	computeService, err := compute.New(c)
	if err != nil {
		setupLog.Error(err, "Failed to init computeService")
		os.Exit(1)
	}

	nodePoolIpManagers := map[string]ipmanager.IpManager{}
	for nodepoolName, ipFilter := range nodePoolIpFilterMapping {
		im, err := ipmanager.InitIpManager(project, region, ipFilter, computeService, refreshInterval)
		if err != nil {
			setupLog.Error(err, "Failed to init ipmanager")
			os.Exit(1)
		}
		nodePoolIpManagers[nodepoolName] = im
	}

	if err = (&controllers.NodeReconciler{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		NodePoolIpManagers: nodePoolIpManagers,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Node")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		for _, ipm := range nodePoolIpManagers {
			ipm.Stop()
		}
		os.Exit(1)
	}
}
