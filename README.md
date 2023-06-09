# alertmanager-stomp-forwarder
###### Dispatching Panic Across the Organization

Prometheus [Alertmanager](https://github.com/prometheus/alertmanager) Webhook Receiver for forwarding alerts to any Stomp receiver. Inspired by https://github.com/DataReply/alertmanager-sns-forwarder.

## Compile

As a Docker image:

```bash
docker build -t alertmanager-stomp-forwarder:0.1 .
```

## Usage

1. Build the Docker image.

2. Deploy, preferably on K8s (yaml provided in folder `deploy`).

3. Configure Alertmanager.

### Arguments

The app accepts some optional arguments, available as flags or env vars.

Flag           | Env Variable              | Default         | Description
---------------|---------------------------|-----------------|------------
`--addr`        | `LISTEN_ADDR` | `0.0.0.0:80`    | Address on which to listen.
`--debug`       | `DEBUG`     | `false`         | Debug mode
`--stomp-addr`  | `STOMP_ADDR`              | localhost:61616 | Address where the stomp server is listening.
`--stomp-user` | `STOMP_USER`              | admin           | User to connect to the stomp server.
`--stomp-pass` | `STOMP_PASS`              | admin           | Pass to connect to the stomp server.

### Endpoints

The app exposes the following HTTP endpoints:

Endpoint         | Method | Description
-----------------|--------|------------
`/alert/<topic>` | `POST` | Endpoint for posting alerts by Alertmanager
`/health`        | `GET`  | Endpoint for k8s readiness and liveness probes
`/metrics`       | `GET`  | Endpoint for Prometheus metrics

### Configuring Alertmanager

Alertmanager configuration file:

```yml
- name: 'sns-forwarder'
  webhook_configs:
  - send_resolved: True
    url: http://<forwarder_url>/alert/<topic_name>
```

Replace `<forwarder_url>` with the correct URL, on K8s using the provided yaml it will be `alertmanager-stomp-forwarder-svc.default:9087`.

### Deploying

In order to deploy the app on K8s the yaml file provided in folder `deploy` can be used. However, the deploy file requires some additional comments.