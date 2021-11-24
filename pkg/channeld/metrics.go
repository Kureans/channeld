package channeld

import (
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

var logger *zap.Logger

var packetReceived = prometheus.NewCounter(
	prometheus.CounterOpts{
		Name: "packets_in",
		Help: "Received packets",
	},
	//[]string{"channel", "msgType"},
)

var packetSent = prometheus.NewCounter(
	prometheus.CounterOpts{
		Name: "packets_out",
		Help: "Sent packets",
	},
	//[]string{"channel", "msgType"},
)

var packetReceiveRate = prometheus.NewGauge(
	prometheus.GaugeOpts{
		Name: "pps_in",
		Help: "Received packets per second",
	},
)

var packetSendRate = prometheus.NewGauge(
	prometheus.GaugeOpts{
		Name: "pps_out",
		Help: "Sent packets per second",
	},
)
var bytesReceived = prometheus.NewCounter(
	prometheus.CounterOpts{
		Name: "bytes_in",
		Help: "Received bytes",
	},
)

var bytesSent = prometheus.NewCounter(
	prometheus.CounterOpts{
		Name: "bytes_out",
		Help: "Sent bytes",
	},
)

var connectionNum = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "connection_num",
		Help: "Number of connections",
	},
	[]string{"type"},
)

var channelNum = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "channel_num",
		Help: "Number of channels",
	},
	[]string{"type"},
)

func InitLogsAndMetrics() {
	logger, _ = zap.NewDevelopment()
	defer logger.Sync()

	prometheus.MustRegister(packetReceived)
	prometheus.MustRegister(packetSent)
	prometheus.MustRegister(packetReceiveRate)
	prometheus.MustRegister(packetSendRate)
	prometheus.MustRegister(bytesReceived)
	prometheus.MustRegister(bytesSent)
	prometheus.MustRegister(connectionNum)
	prometheus.MustRegister(channelNum)
}
