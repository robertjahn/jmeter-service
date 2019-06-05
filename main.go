package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/cloudevents/sdk-go/pkg/cloudevents"
	"github.com/cloudevents/sdk-go/pkg/cloudevents/client"
	cloudeventshttp "github.com/cloudevents/sdk-go/pkg/cloudevents/transport/http"
	"github.com/cloudevents/sdk-go/pkg/cloudevents/types"
	"github.com/google/uuid"
	"github.com/kelseyhightower/envconfig"
	"github.com/keptn/go-utils/pkg/utils"
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

type testFinishedEvent struct {
	Data      interface{}
	StartDate time.Time `json:"startdate"`
}

type evaluationDoneEvent struct {
	Data             interface{}
	EvaluationPassed bool `json:"evaluationpassed"`
}

func gotEvent(ctx context.Context, event cloudevents.Event) error {
	var shkeptncontext string
	event.Context.ExtensionAs("shkeptncontext", &shkeptncontext)

	utils.Debug(shkeptncontext, fmt.Sprintf("Got Event Context: %+v", event.Context))

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

	var res = true
	res, err = runHealthCheck(data, id)
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

	switch strings.ToLower(data.TestStrategy) {
	case "functional":
		res, err = runFunctionalCheck(data, id)
		if err != nil {
			utils.Error(shkeptncontext, err.Error())
			return
		}

	case "performance":
		res, err = runPerformanceCheck(data, id)
		if err != nil {
			utils.Error(shkeptncontext, err.Error())
			return
		}

	default:
		utils.Error(shkeptncontext, "Unknown test strategy '"+data.TestStrategy+"'")
	}

	if !res {
		if err := sendEvaluationDoneEvent(shkeptncontext, event); err != nil {
			utils.Error(shkeptncontext, fmt.Sprintf("Error sending evaluation done event: %s", err.Error()))
		}
		return
	}
	if err := sendTestsFinishedEvent(shkeptncontext, event); err != nil {
		utils.Error(shkeptncontext, fmt.Sprintf("Error sending test finished event: %s", err.Error()))
	}
}

func runHealthCheck(data deploymentFinishedEvent, id string) (bool, error) {
	switch strings.ToLower(data.DeploymentStrategy) {
	case "direct":
		if err := utils.CheckDeploymentRolloutStatus(data.Service, data.Project+"-"+data.Stage); err != nil {
			return false, err
		}

	case "blue_green_service":
		if err := utils.CheckDeploymentRolloutStatus(data.Service+"-blue", data.Project+"-"+data.Stage); err != nil {
			return false, err
		}
		if err := utils.CheckDeploymentRolloutStatus(data.Service+"-green", data.Project+"-"+data.Stage); err != nil {
			return false, err
		}

	default:
		return false, errors.New("Unknown deployment strategy '" + data.DeploymentStrategy + "'")
	}

	os.RemoveAll("HealthCheck_" + data.Service)
	os.RemoveAll("HealthCheck_" + data.Service + "_result.tlf")
	os.RemoveAll("output.txt")

	return executeJMeter(data.Service+"/jmeter/basiccheck.jmx", "HealthCheck_"+data.Service,
		data.Service+"."+data.Project+"-"+data.Stage, 80, "/health", 1, 1, 250, "HealthCheck_"+id,
		true, 0)
}

func runFunctionalCheck(data deploymentFinishedEvent, id string) (bool, error) {

	os.RemoveAll("FuncCheck_" + data.Service)
	os.RemoveAll("FuncCheck_" + data.Service + "_result.tlf")
	os.RemoveAll("output.txt")

	return executeJMeter(data.Service+"/jmeter/"+data.Service+"_load.jmx",
		"FuncCheck_"+data.Service, data.Service+"."+data.Project+"-"+data.Stage+".svc.cluster.local",
		80, "/health", 1, 1, 250, "FuncCheck_"+id, true, 0)
}

func runPerformanceCheck(data deploymentFinishedEvent, id string) (bool, error) {

	os.RemoveAll("PerfCheck_" + data.Service)
	os.RemoveAll("PerfCheck_" + data.Service + "_result.tlf")
	os.RemoveAll("output.txt")

	gateway, err := getGatewayFromConfigmap()
	if err != nil {
		return false, err
	}

	return executeJMeter(data.Service+"/jmeter/"+data.Service+"_load.jmx", "PerfCheck_"+data.Service,
		data.Service+"."+data.Project+"-"+data.Stage+"."+gateway, 80, "/health", 10, 500, 250, "PerfCheck_"+id,
		false, 0)
}

func getGatewayFromConfigmap() (string, error) {
	return utils.ExecuteCommand("kubectl", []string{"get", "configmaps", "keptn-domain",
		"--namespace", "keptn", "-ojsonpath={.data.app_domain}"})
}

func sendTestsFinishedEvent(shkeptncontext string, incomingEvent cloudevents.Event) error {

	source, _ := url.Parse("jmeter-service")
	contentType := "application/json"

	data := testFinishedEvent{Data: incomingEvent.Data, StartDate: incomingEvent.Context.GetTime()}

	event := cloudevents.Event{
		Context: cloudevents.EventContextV02{
			ID:          uuid.New().String(),
			Type:        "sh.keptn.events.tests-finished",
			Source:      types.URLRef{URL: *source},
			ContentType: &contentType,
			Extensions:  map[string]interface{}{"shkeptncontext": shkeptncontext},
		}.AsV02(),
		Data: data,
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

	data := evaluationDoneEvent{Data: incomingEvent.Data, EvaluationPassed: false}

	event := cloudevents.Event{
		Context: cloudevents.EventContextV02{
			ID:          uuid.New().String(),
			Type:        "sh.keptn.events.evaluation-done",
			Source:      types.URLRef{URL: *source},
			ContentType: &contentType,
			Extensions:  map[string]interface{}{"shkeptncontext": shkeptncontext},
		}.AsV02(),
		Data: data,
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
