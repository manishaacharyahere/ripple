package main

import (
	"flag"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	ripplev1 "github.com/manishaacharyahere/ripple/api/v1alpha1"
	"github.com/manishaacharyahere/ripple/internal/controller"
)

func runOperator(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("operator", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ns := fs.String("namespace", "ripple", "namespace to watch; runner NetworkPolicy and ServiceAccount must exist there")
	metrics := fs.String("metrics-addr", ":8080", "Prometheus metrics address")
	probes := fs.String("probe-addr", ":8081", "health probe address")
	leader := fs.Bool("leader-elect", true, "use leader election so only one replica reconciles")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	ctrl.SetLogger(zap.New())

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := ripplev1.AddToScheme(scheme); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		Cache:                   cache.Options{DefaultNamespaces: map[string]cache.Config{*ns: {}}},
		Metrics:                 metricsserver.Options{BindAddress: *metrics},
		HealthProbeBindAddress:  *probes,
		LeaderElection:          *leader,
		LeaderElectionID:        "ripple-operator.ripple.dev",
		LeaderElectionNamespace: *ns,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := (&controller.ReleaseReconciler{Client: mgr.GetClient(), Scheme: scheme}).SetupWithManager(mgr); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_ = mgr.AddHealthzCheck("healthz", healthz.Ping)
	_ = mgr.AddReadyzCheck("readyz", healthz.Ping)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
