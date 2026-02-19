// Package broker provides the server-side entry point for implementing
// Open Service Broker API v2.17 compliant service brokers.
//
// Broker authors implement the [osbapi.ServiceBroker] interface and pass it to
// [NewHandler] to get an [http.Handler] that serves all OSB API endpoints.
//
// Example:
//
//	handler := broker.NewHandler(myBroker,
//	    broker.WithBasicAuth("user", "pass"),
//	    broker.WithLogger(myLogger),
//	)
//	http.ListenAndServe(":8080", handler)
package broker
