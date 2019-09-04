package main

import (
	"encoding/json"
	"github.com/gorilla/mux"
	"github.com/just1689/entity-sync/es"
	"github.com/just1689/entity-sync/es/esq"
	"github.com/just1689/entity-sync/es/shared"
	"github.com/just1689/pg-gateway/client"
	"github.com/just1689/pg-gateway/query"
	"github.com/just1689/tracing"
	"github.com/sirupsen/logrus"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

type PassThroughMsg shared.EntityKey

var entitySync es.EntitySync
var oemPublisher = esq.BuildPublisher(os.Getenv("nsqAddr"))("worker.oems.v1")
var serviceName = "sig-api-v1"

func main() {

	tracing.StartTracing(tracing.Config{
		Url:             os.Getenv("tracingUrl"),
		CacheSize:       1024,
		FlushTimeout:    1,
		FlushSize:       10,
		SleepBetweenErr: 1,
		RetryErr:        true,
	})

	// Provide a configuration
	config := es.Config{
		Mux:           mux.NewRouter(),
		NSQAddr:       os.Getenv("nsqAddr"),
		WSPassThrough: passThrough,
	}
	//Setup entitySync with that configuration
	entitySync = es.Setup(config)

	//Register an entity and tell the library how to fetch and what to write to the client
	entitySync.RegisterEntityAndDBHandler("sigui", func(entityKey shared.EntityKey, secret string, handler shared.ByteHandler) {
		b, _ := json.Marshal(entityKey.ID)
		handler(b)
	})

	//Start a listener and provide the mux for routes / handling
	l, _ := net.Listen("tcp", os.Getenv("listenAddr"))
	http.Serve(l, config.Mux)

}

func passThrough(secret string, b []byte) {
	p := PassThroughMsg{}
	err := json.Unmarshal(b, &p)
	if err != nil {
		logrus.Errorln("error while unmarshaling passThroughMsg from client ws")
		logrus.Errorln(err)
		return
	}

	if strings.Contains(string(p.Entity), "table") {
		//The client is requesting some data
		go sendTableToClient(p.ID, secret)
		return
	}
	if string(p.Entity) == "action" {
		if p.ID == "oem" {
			traceID := tracing.NewId()
			span := tracing.NewSpan(traceID, serviceName, "action.oemPublisher", 0)
			start := time.Now()
			oemPublisher([]byte(traceID))
			span.SetDuration(time.Since(start))
		}
	}

}

func sendTableToClient(table string, secret string) {
	pgg := os.Getenv("PGGW")
	c, err := client.GetEntityManyAsync(pgg, query.Query{
		Entity: "items",
		Comparisons: []query.Comparison{
			query.Comparison{
				Field:      "title",
				Comparator: "eq",
				Value:      table,
			},
		},
	})
	if err != nil {
		logrus.Errorln(err)
		return
	}

	for row := range c {
		entitySync.Bridge.NotifyAllOfChange(
			shared.EntityKey{
				Entity: "sigui",
				ID:     string(row), //TODO: wrap in context
			})
	}

}
