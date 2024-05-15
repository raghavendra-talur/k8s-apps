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
)

func main() {
	setup()

	if startAt == "start" {
		deployApplications()
	}
	if stopAt == "deployapp" {
		return
	}

	if startAt == "enabledr" || startAt == "start" {
		enableDR()
	}
	if stopAt == "enabledr" {
		return
	}

	if startAt == "failover" || startAt == "enabledr" || startAt == "start" {
		failover()
	}
	if stopAt == "failover" {
		return
	}

	if startAt == "relocate" || startAt == "failover" || startAt == "enabledr" || startAt == "start" {
		relocate()
	}
	if stopAt == "relocate" {
		return
	}

	if startAt == "disabledr" || startAt == "relocate" || startAt == "failover" || startAt == "enabledr" || startAt == "start" {
		disableDR()
	}
	if stopAt == "disabledr" {
		return
	}

	if startAt == "deleteapp" || startAt == "disabledr" || startAt == "relocate" || startAt == "failover" || startAt == "enabledr" || startAt == "start" {
		deleteApplications()
	}
	if stopAt == "end" {
		return
	}
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
		stopAt = "enabledr"
	}

	startAt = os.Getenv("START_AT")
	if startAt == "" {
		startAt = "start"
	}

	rbdCountEnv := os.Getenv("DEPLOYMENT_RBD_COUNT")
	if rbdCountEnv == "" {
		rbdCountEnv = "1"
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

	rand.Seed(time.Now().UnixNano())
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
}

func deployApplications() {
	createNamespaces(namespaces, preferredCluster)
	// Deploy resources for RBD and CephFS storage classes
	deployResources("rook-ceph-block", rbdCount, preferredCluster)
	deployResources("rook-cephfs", cephfsCount, preferredCluster)
}

func deleteApplications() {
	deleteResources()

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

	cmd = exec.Command("kubectl", "--context", preferredCluster, "-n", "rook-ceph", "exec", "-it", strings.Trim(string(podName), "'"), "--", "bash", "-c", "rbd ls --pool=replicapool | xargs -n 1 rbd rm --pool=replicapool")
	if err := cmd.Run(); err != nil {
		log.Fatalf("Failed to delete rbd volumes: %v", err)
	}
}

func createNamespaces(namespaces []string, kcontext string) {
	for _, namespace := range namespaces {
		nsYaml := loadTemplate("namespace.yaml", "", "", namespace)
		applyYaml(nsYaml, kcontext)
	}
}

func enableDR() {
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
		cmd = exec.Command("kubectl", "delete", restype, name, "--context", kcontext, "--ignore-not-found")
	} else {
		cmd = exec.Command("kubectl", "delete", restype, name, "-n", namespace, "--context", kcontext, "--ignore-not-found")
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("Failed to delete resource: %v", err)
	}
}

func disableDR() {
	drpcYaml := loadTemplate("drpc.yaml", "", "", "")
	restype, name, ns := getResourceDetailsFromYAML(drpcYaml)
	deleteResource(hub, restype, name, ns)

	placementYaml := loadTemplate("placement.yaml", "", "", "")
	restype, name, ns = getResourceDetailsFromYAML(placementYaml)
	deleteResource(hub, restype, name, ns)

	placementDecisionYaml := loadTemplate("placementdecision.yaml", "", "", "")
	restype, name, ns = getResourceDetailsFromYAML(placementDecisionYaml)
	deleteResource(hub, restype, name, ns)

	deleteResource(hub, "manifestwork", fmt.Sprintf("%s-drpc-%s-ns-mw", testID, ramenOpsNamespace), preferredCluster)
	deleteResource(hub, "manifestwork", fmt.Sprintf("%s-drpc-%s-ns-mw", testID, ramenOpsNamespace), failoverCluster)
}

func failover() {
	drpcYaml := loadTemplate("drpc.yaml", "", "", "")
	applyYaml(drpcYaml, hub)

	resourceName := fmt.Sprintf("drplacementcontrols.ramendr.openshift.io/%v-drpc", testID)
	patchContent := fmt.Sprintf("{\"spec\":{\"action\": \"Failover\"}}")

	err := patchResource(hub, resourceName, ramenOpsNamespace, patchContent)
	if err != nil {
		log.Fatalf("Failed to patch resource status: %v", err)
	}
}

func relocate() {
	log.Default().Println("Relocating resources")

	drpcYaml := loadTemplate("drpc.yaml", "", "", "")
	applyYaml(drpcYaml, hub)

	resourceName := fmt.Sprintf("drplacementcontrols.ramendr.openshift.io/%v-drpc", testID)
	patchContent := fmt.Sprintf("{\"spec\":{\"action\": \"Relocate\"}}")

	err := patchResource(hub, resourceName, ramenOpsNamespace, patchContent)
	if err != nil {
		log.Fatalf("Failed to patch resource status: %v", err)
	}
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

func deleteResources() {
	for _, namespace := range namespaces {
		cmd := exec.Command("kubectl", "delete", "deployment", "--all", "-n", namespace, "--context", preferredCluster, "--ignore-not-found")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatalf("Failed to delete deployments: %v", err)
		}

		cmd = exec.Command("kubectl", "delete", "pvc", "--all", "-n", namespace, "--context", preferredCluster, "--ignore-not-found")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatalf("Failed to delete pvcs: %v", err)
		}
	}

	for _, namespace := range namespaces {
		cmd := exec.Command("kubectl", "delete", "deployment", "--all", "-n", namespace, "--context", failoverCluster, "--ignore-not-found")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatalf("Failed to delete deployments: %v", err)
		}

		cmd = exec.Command("kubectl", "delete", "pvc", "--all", "-n", namespace, "--context", failoverCluster, "--ignore-not-found")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Fatalf("Failed to delete pvcs: %v", err)
		}
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
