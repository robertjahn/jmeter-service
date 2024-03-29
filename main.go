package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cloudevents/sdk-go/pkg/cloudevents"
	"github.com/cloudevents/sdk-go/pkg/cloudevents/client"
	cloudeventshttp "github.com/cloudevents/sdk-go/pkg/cloudevents/transport/http"
	"github.com/cloudevents/sdk-go/pkg/cloudevents/types"
	"github.com/google/uuid"
	"github.com/kelseyhightower/envconfig"
	"github.com/keptn/go-utils/pkg/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type envConfig struct {
	// Port on which to listen for cloudevents
	Port int    `envconfig:"RCV_PORT" default:"8080"`
	Path string `envconfig:"RCV_PATH" default:"/"`
}

func main() {
	var env envConfig
	if err := envconfig.Process("", &env); err != nil {
		log.Fatalf("Failed to process env var: %s", err)
	}
	os.Exit(_main(os.Args[1:], env))
}

type deploymentFinishedEvent struct {
	GitHubOrg          string `json:"githuborg"`
	Project            string `json:"project"`
	TestStrategy       string `json:"teststrategy"`
	DeploymentStrategy string `json:"deploymentstrategy"`
	Stage              string `json:"stage"`
	Service            string `json:"service"`
	Image              string `json:"image"`
	Tag                string `json:"tag"`
}

func gotEvent(ctx context.Context, event cloudevents.Event) error {
	var shkeptncontext string
	event.Context.ExtensionAs("shkeptncontext", &shkeptncontext)

	data := &deploymentFinishedEvent{}
	if err := event.DataAs(data); err != nil {
		utils.Error(shkeptncontext, fmt.Sprintf("Got Data Error: %s", err.Error()))
		return err
	}

	if event.Type() != "sh.keptn.events.deployment-finished" {
		const errorMsg = "Received unexpected keptn event"
		utils.Error(shkeptncontext, errorMsg)
		return errors.New(errorMsg)
	}

	go runTests(event, shkeptncontext, *data)

	return nil
}

func runTests(event cloudevents.Event, shkeptncontext string, data deploymentFinishedEvent) {

	_, err := utils.Checkout(data.GitHubOrg, data.Service, "master")
	if err != nil {
		utils.Error(shkeptncontext, fmt.Sprintf("Error when checkingout from GitHub: %s", err.Error()))
		return
	}

	utils.Info(shkeptncontext, "Running tests with jmeter")

	id := uuid.New().String()

	var res bool
	res, err = runHealthCheck(shkeptncontext, data, id)
	if err != nil {
		utils.Error(shkeptncontext, err.Error())
		return
	}
	if !res {
		if err := sendEvaluationDoneEvent(shkeptncontext, event); err != nil {
			utils.Error(shkeptncontext, fmt.Sprintf("Error sending evaluation done event: %s", err.Error()))
		}
		return
	}

	var sendEvent = false
	startedAt := time.Now()
	switch strings.ToLower(data.TestStrategy) {
	case "functional":
		res, err = runFunctionalCheck(shkeptncontext, data, id)
		if err != nil {
			utils.Error(shkeptncontext, err.Error())
			return
		}
		utils.Info(shkeptncontext, "Functional test result = "+strconv.FormatBool(res))
		sendEvent = true

	case "performance":
		res, err = runPerformanceCheck(shkeptncontext, data, id)
		if err != nil {
			utils.Error(shkeptncontext, err.Error())
			return
		}
		utils.Info(shkeptncontext, "Performance test result = "+strconv.FormatBool(res))
		sendEvent = true

	case "":
		utils.Info(shkeptncontext, "No test strategy specified, hence no tests are triggered.")

	default:
		utils.Error(shkeptncontext, "Unknown test strategy '"+data.TestStrategy+"'")
	}

	if sendEvent {
		if !res {
			if err := sendEvaluationDoneEvent(shkeptncontext, event); err != nil {
				utils.Error(shkeptncontext, fmt.Sprintf("Error sending evaluation done event: %s", err.Error()))
			}
			return
		}
		if err := sendTestsFinishedEvent(shkeptncontext, event, startedAt); err != nil {
			utils.Error(shkeptncontext, fmt.Sprintf("Error sending test finished event: %s", err.Error()))
		}
	}
}

func runHealthCheck(shkeptncontext string, data deploymentFinishedEvent, id string) (bool, error) {
	switch strings.ToLower(data.DeploymentStrategy) {
	case "direct":
		if err := utils.WaitForDeploymentToBeAvailable(true, data.Service, data.Project+"-"+data.Stage); err != nil {
			return false, err
		}

	case "blue_green_service":
		if err := utils.WaitForDeploymentToBeAvailable(true, data.Service+"-blue", data.Project+"-"+data.Stage); err != nil {
			return false, err
		}
		if err := utils.WaitForDeploymentToBeAvailable(true, data.Service+"-green", data.Project+"-"+data.Stage); err != nil {
			return false, err
		}

	default:
		return false, errors.New("Unknown deployment strategy '" + data.DeploymentStrategy + "'")
	}

	os.RemoveAll("HealthCheck_" + data.Service)
	os.RemoveAll("HealthCheck_" + data.Service + "_result.tlf")
	os.RemoveAll("output.txt")

	return executeJMeter(shkeptncontext, data.Service+"/jmeter/basiccheck.jmx", "HealthCheck_"+data.Service,
		data.Service+"."+data.Project+"-"+data.Stage, 80, "/health", 1, 1, 250, "HealthCheck_"+id,
		true, 0)
}

func runFunctionalCheck(shkeptncontext string, data deploymentFinishedEvent, id string) (bool, error) {

	os.RemoveAll("FuncCheck_" + data.Service)
	os.RemoveAll("FuncCheck_" + data.Service + "_result.tlf")
	os.RemoveAll("output.txt")

	return executeJMeter(shkeptncontext, data.Service+"/jmeter/"+data.Service+"_load.jmx",
		"FuncCheck_"+data.Service, data.Service+"."+data.Project+"-"+data.Stage,
		80, "/health", 1, 1, 250, "FuncCheck_"+id, true, 0)
}

func runPerformanceCheck(shkeptncontext string, data deploymentFinishedEvent, id string) (bool, error) {

	os.RemoveAll("PerfCheck_" + data.Service)
	os.RemoveAll("PerfCheck_" + data.Service + "_result.tlf")
	os.RemoveAll("output.txt")

	gateway, err := getGatewayFromConfigmap()
	if err != nil {
		return false, err
	}

	return executeJMeter(shkeptncontext, data.Service+"/jmeter/"+data.Service+"_load.jmx", "PerfCheck_"+data.Service,
		data.Service+"."+data.Project+"-"+data.Stage+"."+gateway, 80, "/health", 10, 500, 250, "PerfCheck_"+id,
		false, 0)
}

func getGatewayFromConfigmap() (string, error) {

	api, err := utils.GetKubeAPI(true)
	if err != nil {
		return "", err
	}

	cm, err := api.ConfigMaps("keptn").Get("keptn-domain", metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	return string(cm.Data["app_domain"]), nil
}

func sendTestsFinishedEvent(shkeptncontext string, incomingEvent cloudevents.Event, startedAt time.Time) error {

	source, _ := url.Parse("jmeter-service")
	contentType := "application/json"

	var testFinishedData interface{}
	if err := incomingEvent.DataAs(&testFinishedData); err != nil {
		utils.Error(shkeptncontext, fmt.Sprintf("Got Data Error: %s", err.Error()))
		return err
	}
	testFinishedData.(map[string]interface{})["startedat"] = startedAt

	event := cloudevents.Event{
		Context: cloudevents.EventContextV02{
			ID:          uuid.New().String(),
			Type:        "sh.keptn.events.tests-finished",
			Source:      types.URLRef{URL: *source},
			ContentType: &contentType,
			Extensions:  map[string]interface{}{"shkeptncontext": shkeptncontext},
		}.AsV02(),
		Data: testFinishedData,
	}

	t, err := cloudeventshttp.New(
		cloudeventshttp.WithTarget("http://event-broker.keptn.svc.cluster.local/keptn"),
		cloudeventshttp.WithEncoding(cloudeventshttp.StructuredV02),
	)
	if err != nil {
		return errors.New("Failed to create transport:" + err.Error())
	}

	c, err := client.New(t)
	if err != nil {
		return errors.New("Failed to create HTTP client:" + err.Error())
	}

	if _, err := c.Send(context.Background(), event); err != nil {
		return errors.New("Failed to send cloudevent:, " + err.Error())
	}
	return nil
}

func sendEvaluationDoneEvent(shkeptncontext string, incomingEvent cloudevents.Event) error {

	source, _ := url.Parse("jmeter-service")
	contentType := "application/json"

	var evaluationDoneData interface{}
	if err := incomingEvent.DataAs(&evaluationDoneData); err != nil {
		utils.Error(shkeptncontext, fmt.Sprintf("Got Data Error: %s", err.Error()))
		return err
	}
	evaluationDoneData.(map[string]interface{})["evaluationpassed"] = false

	event := cloudevents.Event{
		Context: cloudevents.EventContextV02{
			ID:          uuid.New().String(),
			Type:        "sh.keptn.events.evaluation-done",
			Source:      types.URLRef{URL: *source},
			ContentType: &contentType,
			Extensions:  map[string]interface{}{"shkeptncontext": shkeptncontext},
		}.AsV02(),
		Data: evaluationDoneData,
	}

	t, err := cloudeventshttp.New(
		cloudeventshttp.WithTarget("http://event-broker.keptn.svc.cluster.local/keptn"),
		cloudeventshttp.WithEncoding(cloudeventshttp.StructuredV02),
	)
	if err != nil {
		return errors.New("Failed to create transport:" + err.Error())
	}

	c, err := client.New(t)
	if err != nil {
		return errors.New("Failed to create HTTP client:" + err.Error())
	}

	if _, err := c.Send(context.Background(), event); err != nil {
		return errors.New("Failed to send cloudevent:, " + err.Error())
	}
	return nil
}

func _main(args []string, env envConfig) int {

	ctx := context.Background()

	utils.ServiceName = "jmeter-service"

	t, err := cloudeventshttp.New(
		cloudeventshttp.WithPort(env.Port),
		cloudeventshttp.WithPath(env.Path),
	)

	if err != nil {
		log.Fatalf("failed to create transport, %v", err)
	}
	c, err := client.New(t)
	if err != nil {
		log.Fatalf("failed to create client, %v", err)
	}

	log.Printf("will listen on :%d%s\n", env.Port, env.Path)
	log.Fatalf("failed to start receiver: %s", c.StartReceiver(ctx, gotEvent))

	return 0
}
