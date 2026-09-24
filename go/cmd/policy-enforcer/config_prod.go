//go:build !local
// +build !local

package main

import (
	"time"

	"go.uber.org/zap"
)

var logLevel = zap.DebugLevel

var serviceName = "policyEnforcer"

var etcdEndpoints = "http://etcd-0.etcd-headless.core.svc.cluster.local:2379,http://etcd-1.etcd-headless.core.svc.cluster.local:2379,http://etcd-2.etcd-headless.core.svc.cluster.local:2379"

var grpcAddr = "localhost:50051"

var port = ":8080"
var apiVersion = "/api/v1"

var eflintServerPath = "eflint-server"
var eflintModelPath = "./eflint-models/01_interface_policy.eflint"
var eflintTimeout = 60 * time.Second
var eflintStartupDelay = 3 * time.Second
var eflintMinPort = 1025
var eflintMaxPort = 65535
var eflintStateDir = "/app/eflint-states"
var autoStartEflint = true
var eflintPoolSize = 3
var eflintHealthCheckInterval = 10 * time.Second
var eflintAcquireTimeout = 30 * time.Second
