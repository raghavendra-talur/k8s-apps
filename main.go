package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	yaml "gopkg.in/yaml.v2"
)

type state int

const (
	startState state = iota
	deployappState
	enabledrState
	failoverState
	relocateState
	disabledrState
	deleteappState
	endState
)

func (s state) String() string {
	return [...]string{
		"start",
		"deployapp",
		"enabledr",
		"failover",
		"relocate",
		"disabledr",
		"deleteapp",
		"end",
	}[s]
}

func (s state) EnumIndex() int {
	return int(s)
}

func getState(s string) state {
	switch s {
	case "start":
		return startState
	case "deployapp":
		return deployappState
	case "enabledr":
		return enabledrState
	case "failover":
		return failoverState
	case "relocate":
		return relocateState
	case "disabledr":
		return disabledrState
	case "deleteapp":
		return deleteappState
	case "end":
		return endState
	default:
		return startState
	}
}

// Global Variables
var (
	hub               string
	dr1               string
	dr2               string
	rbdCount          int
	cephfsCount       int
	namespacesCount   int
	testID            string
	namespaces        []string
	totalRes          int
	preferredCluster  string
	failoverCluster   string
	ramenOpsNamespace = "ramen-ops"
	stopAt            string
	startAt           string
	startAtState      state
	stopAtState       state
	currentState      = startState
)

func showCurrentState() {
	log.Printf("Current state: %s\n", currentState)
}

func main() {
	setup()
	deployApplications()
	enableDR()
	failover()
	relocate()
	disableDR()
	deleteApplications()
	showCurrentState()
}

func setup() {
	hub = os.Getenv("HUB")
	if hub == "" {
		hub = "rdr-hub"
	}

	dr1 = os.Getenv("DR1")
	if dr1 == "" {
		dr1 = "rdr-dr1"
	}

	dr2 = os.Getenv("DR2")
	if dr2 == "" {
		dr2 = "rdr-dr2"
	}

	preferredCluster = os.Getenv("PREFERRED_CLUSTER")
	if preferredCluster == "" {
		preferredCluster = dr1
	}

	failoverCluster = os.Getenv("FAILOVER_CLUSTER")
	if failoverCluster == "" {
		failoverCluster = dr2
	}

	stopAt = os.Getenv("STOP_AT")
	if stopAt == "" {
		stopAt = "deployapp"
	}
	stopAtState = getState(stopAt)

	startAt = os.Getenv("START_AT")
	if startAt == "" {
		startAt = "start"
	}
	startAtState = getState(startAt)

	rbdCountEnv := os.Getenv("DEPLOYMENT_RBD_COUNT")
	if rbdCountEnv == "" {
		rbdCountEnv = "0"
	}
	rbdCount, _ = strconv.Atoi(rbdCountEnv)

	cephfsCountEnv := os.Getenv("DEPLOYMENT_CEPHFS_COUNT")
	if cephfsCountEnv == "" {
		cephfsCountEnv = "1"
	}
	cephfsCount, _ = strconv.Atoi(cephfsCountEnv)

	namespacesCountEnv := os.Getenv("NAMESPACES_COUNT")
	if namespacesCountEnv == "" {
		namespacesCountEnv = "1"
	}
	namespacesCount, _ = strconv.Atoi(namespacesCountEnv)

	// testID = fmt.Sprintf("test-%d", time.Now().UnixNano()) // Unique test ID
	testID = fmt.Sprintf("test-%d", 1) // Unique test ID
	namespaces = generateNamespaces(namespacesCount)

	// print all the setup info
	log.Printf("HUB: %s\n", hub)
	log.Printf("DR1: %s\n", dr1)
	log.Printf("DR2: %s\n", dr2)
	log.Printf("PREFERRED_CLUSTER: %s\n", preferredCluster)
	log.Printf("FAILOVER_CLUSTER: %s\n", failoverCluster)
	log.Printf("DEPLOYMENT_RBD_COUNT: %d\n", rbdCount)
	log.Printf("DEPLOYMENT_CEPHFS_COUNT: %d\n", cephfsCount)
	log.Printf("NAMESPACES_COUNT: %d\n", namespacesCount)
	log.Printf("TESTID: %s\n", testID)
	log.Printf("NAMESPACES: %v\n", namespaces)
	log.Printf("STOP_AT: %s\n", stopAt)
	log.Printf("START_AT: %s\n", startAt)
	log.Printf("START_AT_STATE: %s\n", startAtState)
	log.Printf("STOP_AT_STATE: %s\n", stopAtState)
}

func deployApplications() {
	if startAtState > deployappState || stopAtState < deployappState {
		return
	}

	createNamespaces(namespaces, preferredCluster)
	createNamespaces(namespaces, failoverCluster)
	// Deploy resources for RBD and CephFS storage classes
	deployResources("rook-ceph-block", rbdCount, preferredCluster)
	deployResources("rook-cephfs", cephfsCount, preferredCluster)
	currentState = deployappState
}

func deleteApplications() {
	if startAtState > deleteappState || stopAtState < deleteappState {
		return
	}
	deleteResources(preferredCluster)
	deleteResources(failoverCluster)

	for _, namespace := range namespaces {
		deleteResource(preferredCluster, "namespace", namespace, "")
		deleteResource(failoverCluster, "namespace", namespace, "")
	}

	// get rook-ceph toolbox and ensure no volumes remain
	// commands are
	// POD_NAME=$(kubectl get pods -n rook-ceph -l app=rook-ceph-tools -o jsonpath='{.items[0].metadata.name}')
	// kubectl -n rook-ceph exec -it $POD_NAME -- bash -c 'rbd ls --pool=replicapool | xargs -n 1 rbd rm --pool=replicapool'

	cmd := exec.Command("kubectl", "--context", preferredCluster, "get", "pods", "-n", "rook-ceph", "-l", "app=rook-ceph-tools", "-o", "jsonpath='{.items[0].metadata.name}'")
	podName, err := cmd.Output()
	if err != nil {
		log.Fatalf("Failed to get pod name: %v", err)
	}

	var rbdCount int
	cmd = exec.Command("kubectl", "--context", preferredCluster, "-n", "rook-ceph", "exec", "-it", strings.Trim(string(podName), "'"), "--", "bash", "-c", "rbd ls --pool=replicapool | wc -l")
	out, err := cmd.Output()
	if err == nil {
		rbdCount, _ = strconv.Atoi(strings.Trim(string(out), "\n"))
	}
	if rbdCount == 0 {
		log.Printf("No rbd volumes to delete")
	}

	if rbdCount > 0 {
		cmd = exec.Command("kubectl", "--context", preferredCluster, "-n", "rook-ceph", "exec", "-it", strings.Trim(string(podName), "'"), "--", "bash", "-c", "rbd ls --pool=replicapool | xargs -n 1 rbd rm --pool=replicapool")
		if err := cmd.Run(); err != nil {
			log.Printf("command failed: %s", cmd.String())
			log.Fatalf("Failed to delete rbd volumes: %v", err)
		}
	}
	currentState = deleteappState
}

func createNamespaces(namespaces []string, kcontext string) {
	for _, namespace := range namespaces {
		nsYaml := loadTemplate("namespace.yaml", "", "", namespace)
		applyYaml(nsYaml, kcontext)
	}
}

func enableDR() {
	if startAtState > enabledrState || stopAtState < enabledrState {
		return
	}
	drpcYaml := loadTemplate("drpc.yaml", "", "", "")
	placementYaml := loadTemplate("placement.yaml", "", "", "")
	placementDecisionYaml := loadTemplate("placementdecision.yaml", "", "", "")
	applyYaml(drpcYaml, hub)
	applyYaml(placementYaml, hub)
	applyYaml(placementDecisionYaml, hub)

	// use kubectl edit-status to set the status of the placementdecision to
	//   decisions:
	//     - clusterName: ${PREFERRED_CLUSTER}
	//       reason: ${PREFERRED_CLUSTER}

	//cmd := exec.Command("kubectl", "edit-status", "-n", ramenOpsNamespace, "--context", hub, "placementdecision", fmt.Sprintf("%v-placement-decision-1", testID))
	//cmd.Stdout = os.Stdout
	//cmd.Stderr = os.Stderr
	//if err := cmd.Run(); err != nil {
	//	log.Fatalf("Failed to apply yaml: %v", err)
	//}

	resourceName := fmt.Sprintf("placementdecision/%v-placement-decision-1", testID)
	// JSON patch content to update the status
	patchContent := fmt.Sprintf("{\"status\":{\"decisions\":[{\"clusterName\": \"%s\",\"reason\": \"%s\"}]}}", preferredCluster, preferredCluster)

	err := patchResourceStatus(hub, resourceName, ramenOpsNamespace, patchContent)
	if err != nil {
		log.Fatalf("Failed to patch resource status: %v", err)
	}
	currentState = enabledrState
}

// getResourceDetailsFromYAML returns the resource type, name and namespace of the kubernetes resource from a single resource yaml
func getResourceDetailsFromYAML(yamlObj string) (string, string, string) {
	// load the yaml into a map
	yamlMap := make(map[interface{}]interface{})
	err := yaml.Unmarshal([]byte(yamlObj), &yamlMap)
	if err != nil {
		log.Fatalf("Failed to unmarshal yaml: %v", err)
	}

	metadata := yamlMap["metadata"].(map[interface{}]interface{})
	name := metadata["name"].(string)

	namespace := metadata["namespace"].(string)

	resourceType := yamlMap["kind"]

	return resourceType.(string), name, namespace
}

func deleteResource(kcontext, restype, name, namespace string) {
	var cmd *exec.Cmd

	if namespace == "" {
		cmd = exec.Command("kubectl", "delete", restype, name, "--context", kcontext, "--ignore-not-found", "--wait=false")
	} else {
		cmd = exec.Command("kubectl", "delete", restype, name, "-n", namespace, "--context", kcontext, "--ignore-not-found", "--wait=false")
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("command failed: %s", cmd.String())
		log.Fatalf("Failed to delete resource: %v", err)
	}
}

func removeFinalizer(kcontext, restype, name, namespace string) {
	time.Sleep(100 * time.Millisecond)

	// check if resource exists, if not return
	cmd := exec.Command("kubectl", "get", restype, name, "-n", namespace, "--context", kcontext)
	if err := cmd.Run(); err != nil {
		log.Printf("Resource not found, skipping finalizer removal")
		return
	}

	cmd = exec.Command("kubectl", "patch", restype, name, "-n", namespace, "--type", "json", "-p", "[{\"op\": \"remove\", \"path\": \"/metadata/finalizers\"}]", "--context", kcontext)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Printf("command failed: %s", cmd.String())
		log.Fatalf("Failed to remove finalizer: %v", err)
	}
}

func disableDR() {
	if startAtState > disabledrState || stopAtState < disabledrState {
		return
	}
	drpcYaml := loadTemplate("drpc.yaml", "", "", "")
	restype, name, ns := getResourceDetailsFromYAML(drpcYaml)
	deleteResource(hub, restype, name, ns)
	removeFinalizer(hub, restype, name, ns)

	placementYaml := loadTemplate("placement.yaml", "", "", "")
	restype, name, ns = getResourceDetailsFromYAML(placementYaml)
	deleteResource(hub, restype, name, ns)
	removeFinalizer(hub, restype, name, ns)

	placementDecisionYaml := loadTemplate("placementdecision.yaml", "", "", "")
	restype, name, ns = getResourceDetailsFromYAML(placementDecisionYaml)
	deleteResource(hub, restype, name, ns)
	removeFinalizer(hub, restype, name, ns)

	deleteResource(hub, "manifestwork", fmt.Sprintf("%s-drpc-%s-ns-mw", testID, ramenOpsNamespace), preferredCluster)
	deleteResource(hub, "manifestwork", fmt.Sprintf("%s-drpc-%s-ns-mw", testID, ramenOpsNamespace), failoverCluster)
	currentState = disabledrState
}

func failover() {
	if startAtState > failoverState || stopAtState < failoverState {
		return
	}
	drpcYaml := loadTemplate("drpc.yaml", "", "", "")
	applyYaml(drpcYaml, hub)

	resourceName := fmt.Sprintf("drplacementcontrols.ramendr.openshift.io/%v-drpc", testID)
	patchContent := fmt.Sprintf("{\"spec\":{\"action\": \"Failover\"}}")

	err := patchResource(hub, resourceName, ramenOpsNamespace, patchContent)
	if err != nil {
		log.Fatalf("Failed to patch resource status: %v", err)
	}
	currentState = failoverState
}

func relocate() {
	if startAtState > relocateState || stopAtState < relocateState {
		return
	}
	log.Default().Println("Relocating resources")

	drpcYaml := loadTemplate("drpc.yaml", "", "", "")
	applyYaml(drpcYaml, hub)

	resourceName := fmt.Sprintf("drplacementcontrols.ramendr.openshift.io/%v-drpc", testID)
	patchContent := fmt.Sprintf("{\"spec\":{\"action\": \"Relocate\"}}")

	err := patchResource(hub, resourceName, ramenOpsNamespace, patchContent)
	if err != nil {
		log.Fatalf("Failed to patch resource status: %v", err)
	}
	currentState = relocateState
}

func generateNamespaces(count int) []string {
	var namespaces []string
	for i := 0; i < count; i++ {
		namespace := fmt.Sprintf("%s-ns-%d", testID, i)
		namespaces = append(namespaces, namespace)
	}
	return namespaces
}

func createResources(storageClass string, count int) []string {
	var resources []string
	for i := totalRes; i < totalRes+count; i++ {
		namespace := namespaces[rand.Intn(len(namespaces))] // Randomly select a namespace
		deploymentName := fmt.Sprintf("%s-w-%d", namespace, i)

		deploymentYaml := loadTemplate("deployment.yaml", deploymentName, storageClass, namespace)
		pvcYaml := loadTemplate("pvc.yaml", deploymentName, storageClass, namespace)

		resources = append(resources, deploymentYaml)
		resources = append(resources, pvcYaml)

	}
	totalRes += count

	return resources
}

func getResources(cluster, namespace, resourceType string) []string {
	cmd := exec.Command("kubectl", "get", resourceType, "-n", namespace, "--context", cluster, "-o", "jsonpath='{.items[*].metadata.name}'")
	out, err := cmd.Output()
	if err != nil {
		log.Fatalf("Failed to get %s: %v", resourceType, err)
	}
	resources := strings.Split(strings.Trim(string(out), "'"), " ")

	filteredResources := []string{}
	for _, name := range resources {
		if name != "" {
			filteredResources = append(filteredResources, name)
		}
	}

	return filteredResources
}

func forceDeleteResources(cluster, namespace, resourceType string) {
	resources := getResources(cluster, namespace, resourceType)
	for _, resource := range resources {
		deleteResource(cluster, resourceType, resource, namespace)
	}

	time.Sleep(1 * time.Second)
	resources = getResources(cluster, namespace, resourceType)
	for _, resource := range resources {
		removeFinalizer(cluster, resourceType, resource, namespace)
	}
}

func deleteResources(cluster string) {
	for _, namespace := range namespaces {
		forceDeleteResources(cluster, namespace, "deployment")
		forceDeleteResources(cluster, namespace, "pvc")
		forceDeleteResources(cluster, namespace, "volumereplication")
	}
}

func deployResources(storageClass string, count int, context string) {
	resources := createResources(storageClass, count)

	for _, yamlres := range resources {
		applyYaml(yamlres, context)
	}
}

func loadTemplate(filename, deploymentName, storageClass, namespace string) string {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		log.Fatalf("Failed to read %s: %v", filename, err)
	}
	yamlContent := string(data)
	yamlContent = strings.ReplaceAll(yamlContent, "${DEPLOYMENT_NAME}", deploymentName)
	yamlContent = strings.ReplaceAll(yamlContent, "${TESTID}", testID)
	yamlContent = strings.ReplaceAll(yamlContent, "${STORAGECLASS}", storageClass)
	yamlContent = strings.ReplaceAll(yamlContent, "${RAMENOPSNAMESPACE}", ramenOpsNamespace)
	yamlContent = strings.ReplaceAll(yamlContent, "${NAMESPACE}", namespace)
	yamlContent = strings.ReplaceAll(yamlContent, "${PREFERRED_CLUSTER}", preferredCluster)
	yamlContent = strings.ReplaceAll(yamlContent, "${FAILOVER_CLUSTER}", failoverCluster)

	if filename == "pvc.yaml" && storageClass == "rook-cephfs" {
		yamlContent = strings.ReplaceAll(yamlContent, "${ACCESS_MODE}", "ReadWriteMany")
	} else {
		yamlContent = strings.ReplaceAll(yamlContent, "${ACCESS_MODE}", "ReadWriteOnce")
	}

	if filename == "drpc.yaml" {
		yamlContent = strings.ReplaceAll(yamlContent, "${PROTECTED_NAMESPACES}", formatProtectedNamespaces(namespaces))
	}

	return yamlContent
}

func formatProtectedNamespaces(namespaces []string) string {
	var result string
	for _, ns := range namespaces {
		result += fmt.Sprintf("    - %s\n", ns)
	}
	return result
}

func applyYaml(yaml, context string) {
	cmd := exec.Command("kubectl", "apply", "-f", "-", "--context", context)
	cmd.Stdin = bytes.NewBufferString(yaml)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("Failed to apply yaml: %v", err)
	}
}

func patchResourceStatus(context, resourceName, resourceNamespace, patchContent string) error {
	cmd := exec.Command("kubectl", "patch", resourceName, "-n", resourceNamespace, "--type", "merge", "--patch", patchContent, "--subresource=status", "--context", context)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func patchResource(context, resourceName, resourceNamespace, patchContent string) error {
	cmd := exec.Command("kubectl", "patch", resourceName, "-n", resourceNamespace, "--type", "merge", "--patch", patchContent, "--context", context)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
