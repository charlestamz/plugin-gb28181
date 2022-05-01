package gb28181

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"github.com/logrusorgru/aurora"
	"github.com/pion/rtp"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/Monibuca/utils/v3"
	"github.com/ghettovoice/gosip"
	"github.com/ghettovoice/gosip/log"
	"github.com/ghettovoice/gosip/sip"
)

var udpsrv gosip.Server
var tcpsrv gosip.Server

type Server struct {
	Ignores    map[string]struct{}
	publishers Publishers
}

const MaxRegisterCount = 3

func FindChannel(deviceId string, channelId string) (c *Channel) {
	if v, ok := Devices.Load(deviceId); ok {
		d := v.(*Device)
		d.channelMutex.RLock()
		c = d.channelMap[channelId]
		d.channelMutex.RUnlock()
	}
	return
}

type Publishers struct {
	data map[uint32]*Publisher
	sync.RWMutex
}

func (p *Publishers) Add(key uint32, pp *Publisher) {
	p.Lock()
	p.data[key] = pp
	p.Unlock()
}
func (p *Publishers) Remove(key uint32) {
	p.Lock()
	delete(p.data, key)
	p.Unlock()
}
func (p *Publishers) Get(key uint32) *Publisher {
	p.RLock()
	defer p.RUnlock()
	return p.data[key]
}

func GetSipServer(transport string) *gosip.Server {
	if strings.ToLower(transport) == "tcp" {
		return &tcpsrv
	}
	return &udpsrv
}

func (s *GB28181Config) startServer() {
	s.publishers.data = make(map[uint32]*Publisher)

	logger := log.NewDefaultLogrusLogger().WithPrefix("GB SIP Server")
	if s.LogVerbose {
		logger.SetLevel(log.DebugLevel)
	}
	srvConf := gosip.ServerConfig{}

	udpsrv = gosip.NewServer(srvConf, nil, nil, logger)
	udpsrv.OnRequest(sip.REGISTER, OnRegister)
	udpsrv.OnRequest(sip.MESSAGE, OnMessage)
	udpsrv.OnRequest(sip.NOTIFY, OnNotify)
	udpsrv.OnRequest(sip.BYE, onBye)

	addr := config.ListenAddr
	Println(fmt.Sprint(aurora.Green("Server gb28181 start at"), aurora.BrightBlue(addr)))
	go udpsrv.Listen("udp", addr)

	if config.TCP {
		tcpSrvConf := gosip.ServerConfig{}

		tcpsrv = gosip.NewServer(tcpSrvConf, nil, nil, logger)
		tcpsrv.OnRequest(sip.REGISTER, OnRegister)
		tcpsrv.OnRequest(sip.MESSAGE, OnMessage)
		tcpsrv.OnRequest(sip.NOTIFY, OnNotify)
		tcpsrv.OnRequest(sip.BYE, onBye)
		go tcpsrv.Listen("tcp", addr)
	}
	go s.startMediaServer()

	if s.Username != "" || s.Password != "" {
		go removeBanDevice(s)
	}
}

func (s *GB28181Config) startMediaServer() {
	if s.IsMediaNetworkTCP() {
		go listenMediaTCP(s)
	}
	listenMediaUDP(s)

}

func processTcpMediaConn(config *GB28181Config, conn net.Conn) {
	var rtpPacket rtp.Packet
	reader := bufio.NewReader(conn)
	lenBuf := make([]byte, 2)
	defer conn.Close()
	var err error
	for err == nil {
		if _, err = io.ReadFull(reader, lenBuf); err != nil {
			return
		}
		ps := make([]byte, binary.BigEndian.Uint16(lenBuf))
		if _, err = io.ReadFull(reader, ps); err != nil {
			return
		}
		if err := rtpPacket.Unmarshal(ps); err != nil {
			Println("gb28181 decode rtp error:", err)
		} else if publisher := config.publishers.Get(rtpPacket.SSRC); publisher != nil && publisher.Err() == nil {
			publisher.PushPS(&rtpPacket)
		}
	}
}

func listenMediaTCP(config *GB28181Config) {
	addr := ":" + strconv.Itoa(int(config.MediaPort))
	mediaAddr, _ := net.ResolveTCPAddr("tcp", addr)
	listen, err := net.ListenTCP("tcp", mediaAddr)

	if err != nil {
		Println("listen media server tcp err", err)
		return
	}
	Printf("tcp server start listen video port[%d]", config.MediaPort)
	defer listen.Close()
	defer Printf("tcp server stop listen video port[%d]", config.MediaPort)

	for {
		conn, err := listen.Accept()
		if err != nil {
			Println("gb28181 decode rtp error:", err)
		}
		go processTcpMediaConn(config, conn)
	}
}

func listenMediaUDP(config *GB28181Config) {
	var rtpPacket rtp.Packet
	networkBuffer := 1048576

	addr := "0.0.0.0:" + strconv.Itoa(int(config.MediaPort))
	mediaAddr, _ := net.ResolveUDPAddr("udp", addr)
	conn, err := net.ListenUDP("udp", mediaAddr)

	if err != nil {
		Printf("listen udp %s err: %v", addr, err)
		return
	}
	bufUDP := make([]byte, networkBuffer)
	Printf("udp server start listen video port[%d]", config.MediaPort)
	defer Printf("udp server stop listen video port[%d]", config.MediaPort)
	for n, _, err := conn.ReadFromUDP(bufUDP); err == nil; n, _, err = conn.ReadFromUDP(bufUDP) {
		ps := bufUDP[:n]
		if err := rtpPacket.Unmarshal(ps); err != nil {
			Println("gb28181 decode rtp error:", err)
		}
		if publisher := config.publishers.Get(rtpPacket.SSRC); publisher != nil && publisher.Err() == nil {
			publisher.PushPS(&rtpPacket)
		}
	}
}

// func queryCatalog(serverConfig *transaction.Config) {
// 	t := time.NewTicker(time.Duration(serverConfig.CatalogInterval) * time.Second)
// 	for range t.C {
// 		Devices.Range(func(key, value interface{}) bool {
// 			device := value.(*Device)
// 			if time.Since(device.UpdateTime) > time.Duration(serverConfig.RegisterValidity)*time.Second {
// 				Devices.Delete(key)
// 			} else if device.Channels != nil {
// 				go device.Catalog()
// 			}
// 			return true
// 		})
// 	}
// }

func removeBanDevice(config *GB28181Config) {
	t := time.NewTicker(time.Duration(config.RemoveBanInterval) * time.Second)
	for range t.C {
		DeviceRegisterCount.Range(func(key, value interface{}) bool {
			if value.(int) > MaxRegisterCount {
				DeviceRegisterCount.Delete(key)
			}
			return true
		})
	}
}
