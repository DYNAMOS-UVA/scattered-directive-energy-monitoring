//go:build local
// +build local

package main

import (
	"fmt"
	"path/filepath"
	"runtime"
	"time"

	"go.uber.org/zap"
)

var logLevel = zap.DebugLevel

var serviceName = "policyEnforcer"

var etcdEndpoints = "http://localhost:30005"

var grpcAddr = "localhost:50051"

var port = ":8082"
var apiVersion = "/api/v1"

var eflintServerPath = "eflint-server"
var eflintModelPath = addPolicyEnforcerDir("eflint/01_interface_policy.eflint")
var eflintTimeout = 60 * time.Second
var eflintStartupDelay = 3 * time.Second
var eflintMinPort = 1025
var eflintMaxPort = 65535
var eflintStateDir = addPolicyEnforcerDir("eflint-states")
var autoStartEflint = true
var eflintPoolSize = 3
var eflintHealthCheckInterval = 10 * time.Second
var eflintAcquireTimeout = 30 * time.Second

func addPolicyEnforcerDir(val string) string {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Println("error")
	}
	dir := filepath.Dir(filename)

	path := fmt.Sprintf("%s/%s", filepath.Clean(filepath.Join(dir, "./")), val)
	return path
}
