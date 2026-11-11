// Package message adds a RabbitMQ-backed messaging transport to an nForge
// application. It separates the connection resource from consumer ingress so
// workers and HTTP handlers can drain before the broker connection closes.
package message
