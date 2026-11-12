// Package sse delivers Server-Sent Events to authenticated users.
//
// It is an explicitly registered infrastructure module (sse.Module) with two
// interchangeable distribution drivers, memory and rabbitmq, and one
// publication API, Manager.PublishToUser. Delivery is user-targeted only: no
// broadcast topics, groups, persistence, offline delivery, replay or
// acknowledgments from browsers. An event published while its user is not
// connected is discarded, and Last-Event-ID is only a reconnection hint.
//
// The HTTP connections are always held by the instance that accepted them. With
// the rabbitmq driver every instance subscribes to one fanout exchange through
// an exclusive, auto-delete queue and filters by user locally, so a user with
// tabs on several instances receives the event on all of them.
//
// The package owns connections, bounded queues, heartbeats and shutdown. It
// never owns the HTTP listener or the RabbitMQ connection: those belong to the
// nhttp and message modules it requires.
package sse
