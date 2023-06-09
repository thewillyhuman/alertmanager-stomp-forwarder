// Inspired in https://github.com/DataReply/alertmanager-sns-forwarder/blob/master/main.go

// Command alertmanager-stomp-forwarder provides a Prometheus Alertmanager Webhook Receiver for forwarding alerts to any
// platform that supports stomp, like ActiveMQ.
package main

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/go-stomp/stomp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"gopkg.in/alecthomas/kingpin.v2"
	"io"
	"net/http"
	"os"
	"strconv"
)

// Alerts is a structure for grouping Prometheus Alerts
type Alerts struct {
	Alerts            []Alert                `json:"alerts"`
	CommonAnnotations map[string]interface{} `json:"commonAnnotations"`
	CommonLabels      map[string]interface{} `json:"commonLabels"`
	ExternalURL       string                 `json:"externalURL"`
	GroupLabels       map[string]interface{} `json:"groupLabels"`
	Receiver          string                 `json:"receiver"`
	Status            string                 `json:"status"`
}

// Alert is a structure for a single Prometheus Alert
type Alert struct {
	Annotations  map[string]interface{} `json:"annotations"`
	EndsAt       string                 `json:"endsAt"`
	GeneratorURL string                 `json:"generatorURL"`
	Labels       map[string]string      `json:"labels"`
	StartsAt     string                 `json:"startsAt"`
}

var (
	log        = logrus.New()
	listenAddr = kingpin.Flag("addr", "Address on which to listen").Default("0.0.0.0:80").Envar("LISTEN_ADDR").String()
	debug      = kingpin.Flag("debug", "Debug mode").Default("false").Envar("DEBUG").Bool()
	stompAddr  = kingpin.Flag("stomp-addr", "Address where the stomp server is listening").Default("localhost:61616").Envar("STOMP_ADDR").String()
	stompUser  = kingpin.Flag("stomp-user", "Username to authenticate in the stomp server").Default("admin").Envar("STOMP_USER").String()
	stompPass  = kingpin.Flag("stomp-pass", "Password to authenticate in the stomp server").Default("admin").Envar("STOMP_PASS").String()

	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "http_response_time_seconds",
		Help: "Duration of HTTP requests.",
	}, []string{})

	httpCounter = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_request_total",
		Help: "Total number of http requests",
	}, []string{"response_code"})

	amqRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "amq_total_requests",
		Help: "Total number of total requests done to activeMQ",
	}, []string{"result"})
)

// This is the main entrypoint of the application. It parses the arguments of the program, sets up the logging
// configuration, sets the router and starts it to listen on the given address.
func main() {
	// Step 1. Parse all the arguments given to the application
	kingpin.Parse()
	log.Printf("configuration {addr=[%s] debug=[%t] amq-addr=[%s] amq-user=[%s], stompPass=[%s]}",
		*listenAddr, *debug, *stompAddr, *stompUser, *stompPass)

	// Step 2. Set up the logging with the parsed config
	setupLogging(*debug)

	// Step 4. Set up the router and start the server to listen on the given address.
	router := createConfiguredRouter()
	log.Infof("listening on address [%s]", *listenAddr)
	err := router.Run(*listenAddr)
	if err != nil {
		log.Fatalf("impossible to initialise router: %s", err)
		os.Exit(-1)
	}
}

// Sets the log level to either debug or release. If the received parameter debugMode is true then the debug level is
// set up. Otherwise, release.
func setupLogging(debugMode bool) {
	if debugMode {
		log.SetLevel(logrus.DebugLevel)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}
}

// This function creates the routes between the different endpoints of the application and the methods that will
// dispatch them.
func createConfiguredRouter() *gin.Engine {
	// Step 1. Create the empty gin router
	router := gin.New()

	// Step 2. Add a middleware that intercepts the calls and logs them. Exclude the health and metrics endpoints
	// from logging. Also add a recovery middleware that in case of any panic it will return a 500 as if there was one.
	router.Use(gin.LoggerWithWriter(gin.DefaultWriter, "/health", "/metrics"))
	router.Use(gin.Recovery())

	// Step 3. Register the routings.
	router.GET("/health", healthGETHandler)
	router.GET("/metrics", prometheusHandler())
	router.POST("/alerts/:topic", alertPOSTHandler)

	// Step 4. Return the configured router
	return router
}

// The health handler is in charge of posting a very simple ok message so that when used from kubernetes the pod can be
// live-health-ready proved.
func healthGETHandler(requestContext *gin.Context) {
	requestContext.JSON(200, gin.H{
		"health": "ok",
	})
}

// The prometheus handler exposes the metrics of the application so that they can be scraped by a prometheus instance.
func prometheusHandler() gin.HandlerFunc {
	prometheusHandler := promhttp.Handler()
	return func(requestContext *gin.Context) {
		prometheusHandler.ServeHTTP(requestContext.Writer, requestContext.Request)
	}
}

// This function is executed each time a post request is made to the '/alert' endpoint. This function should be
// executed each time the alert-manager throws a webhook. It gets the topic as a parameter of the request '/alert/:topic'
// and the alarm contents from the body of the request. Then it posts the alert in the given ActiveMQ topic.
//
// If during the parsing of the topic, alert or during the posting of the alert in ActiveMQ there is any error, then
// an error is raised and the request is answered with a 500.
func alertPOSTHandler(requestContext *gin.Context) {
	// Step 1. Start the timer to instrument the request
	timer := prometheus.NewTimer(httpDuration.WithLabelValues())

	// Step 2. From the request extract the topic and the alert body
	topic := requestContext.Params.ByName("topic")
	requestBody, err := io.ReadAll(requestContext.Request.Body)
	if err != nil {
		timer.ObserveDuration()
		httpCounter.WithLabelValues(strconv.Itoa(http.StatusInternalServerError)).Inc()
		requestContext.Writer.WriteHeader(http.StatusInternalServerError)
		log.Fatalf("the request body could not be extracted")
		return
	}

	// Step 3. Transform the body request to a set of alerts
	alerts, err := unmarshalAlerts(requestBody)
	if err != nil {
		timer.ObserveDuration()
		httpCounter.WithLabelValues(strconv.Itoa(http.StatusInternalServerError)).Inc()
		requestContext.Writer.WriteHeader(http.StatusInternalServerError)
		log.Fatalf("the request body could not be unmarshalled to an alerts object. reuqest body: %s. err: %s",
			string(requestBody), err)
		return
	}

	// Step 4. Send the alerts to activeMQ
	for _, alert := range alerts.Alerts {
		err := sendAlertToStomp(topic, alert)
		if err != nil {
			timer.ObserveDuration()
			amqRequests.WithLabelValues("not_ok").Inc()
			log.Fatalf("request for alert %s not successful", alert)
		}
		amqRequests.WithLabelValues("ok").Inc()
	}

	// Step 5. Finish the request.
	timer.ObserveDuration()
	httpCounter.WithLabelValues(strconv.Itoa(http.StatusOK)).Inc()
	requestContext.Writer.WriteHeader(http.StatusOK)
}

// From the body request, a set of bytes, obtain the alert objects.
func unmarshalAlerts(requestBody []byte) (Alerts, error) {
	var alerts Alerts
	err := json.Unmarshal(requestBody, &alerts)
	if err != nil {
		return alerts, err
	}
	return alerts, nil
}

// Sends a single alert to the stomp endpoint. From the alert are extracted the topic and the required headers for
// Alertmanager.
func sendAlertToStomp(topic string, alert Alert) error {
	message, err := json.Marshal(alert)
	if err != nil {
		log.Fatalf("error while marshalling alert")
		return err
	}

	log.Infof("amq request {topic: %s, message: %s}", topic, message)
	stompConn, err := stomp.Dial("tcp", *stompAddr, stomp.ConnOpt.Login(*stompUser, *stompPass))
	if err != nil {
		log.Fatalf("error while connecting to stomp: %s", err)
	} else {
		log.Infof("connected to stomp endpoint")
	}

	err = stompConn.Send(topic, "application/json", message)
	if err != nil {
		log.Fatalf("failed to send message to ActiveMQ broker: %v", err)
		return err
	}

	_ = stompConn.Disconnect()
	return nil
}
