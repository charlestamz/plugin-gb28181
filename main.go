package gb28181

import (
	"github.com/sirupsen/logrus"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	_ "sync"
	"time"

	"github.com/Monibuca/engine/v3"
	. "github.com/Monibuca/utils/v3"
	"github.com/ghettovoice/gosip/sip"
)

type GB28181Config struct {
	AutoInvite     bool
	AutoCloseAfter int
	PreFetchRecord bool

	//sip服务器的配置
	SipNetwork string //传输协议，默认UDP，可选TCP
	SipIP      string //sip 服务器公网IP
	SipPort    uint16 //sip 服务器端口，默认 5060
	Serial     string //sip 服务器 id, 默认 34020000002000000001
	Realm      string //sip 服务器域，默认 3402000000
	Username   string //sip 服务器账号
	Password   string //sip 服务器密码

	AckTimeout        uint16 //sip 服务应答超时，单位秒
	RegisterValidity  int    //注册有效期，单位秒，默认 3600
	RegisterInterval  int    //注册间隔，单位秒，默认 60
	HeartbeatInterval int    //心跳间隔，单位秒，默认 60
	HeartbeatRetry    int    //心跳超时次数，默认 3

	//媒体服务器配置
	MediaIP          string //媒体服务器地址
	MediaPort        uint16 //媒体服务器端口
	MediaNetwork     string //媒体传输协议，默认UDP，可选TCP
	MediaPortMin     uint16
	MediaPortMax     uint16
	MediaIdleTimeout uint16 //推流超时时间，超过则断开链接，让设备重连

	AudioEnable       bool //是否开启音频
	LogVerbose        bool
	WaitKeyFrame      bool //是否等待关键帧，如果等待，则在收到第一个关键帧之前，忽略所有媒体流
	RemoveBanInterval int  //移除禁止设备间隔
	UdpCacheSize      int  //udp缓存大小

	Server
}

func (s *GB28181Config) IsMediaNetworkTCP() bool {
	return strings.ToLower(s.MediaNetwork) == "tcp"
}

var serverConfig = &GB28181Config{

	AutoInvite:     true,
	AutoCloseAfter: -1,
	PreFetchRecord: false,
	UdpCacheSize:   0,
	SipNetwork:     "udp",
	SipIP:          "127.0.0.1",
	SipPort:        5060,
	Serial:         "34020000002000000001",
	Realm:          "3402000000",
	Username:       "",
	Password:       "",

	AckTimeout:        10,
	RegisterValidity:  60,
	RegisterInterval:  60,
	HeartbeatInterval: 60,
	HeartbeatRetry:    3,

	MediaIP:          "127.0.0.1",
	MediaPort:        58200,
	MediaIdleTimeout: 30,
	MediaNetwork:     "udp",

	RemoveBanInterval: 600,
	LogVerbose:        false,
	AudioEnable:       true,
	WaitKeyFrame:      true,
}

var config = struct {
	Serial            string
	Realm             string
	ListenAddr        string
	Expires           int
	SipIP             string
	SipPort           uint16
	MediaIP           string
	MediaPort         uint16
	AutoInvite        bool
	AutoCloseAfter    int
	Ignore            []string
	TCP               bool
	TCPMediaPortNum   uint16
	RemoveBanInterval int
	PreFetchRecord    bool
	Username          string
	Password          string
	UdpCacheSize      int //udp排序缓存
	LogVerbose        bool
}{"34020000002000000001", "3402000000", "0.0.0.0:5060", 3600, "0.0.0.0", 5060, "0.0.0.0", 58200, false, -1, nil, false, 1, 600, false, "", "", 0, false}

func init() {
	pc := engine.PluginConfig{
		Name:   "GB28181",
		Config: &config,
	}
	pc.Install(run)
}

func storeDevice(id string, req sip.Request, tx *sip.ServerTransaction) {
	var d *Device
	from, _ := req.From()

	deviceAddr := sip.Address{
		DisplayName: from.DisplayName,
		Uri:         from.Address,
	}
	if _d, loaded := Devices.Load(id); loaded {
		d = _d.(*Device)
		d.UpdateTime = time.Now()
		d.Addr = req.Source()
		d.addr = deviceAddr
	} else {
		//猜测终端看到的服务器ip地址
		ipAddr, err := net.ResolveUDPAddr("", req.Destination())
		guessIp := ipAddr.IP.String()
		guessPort := uint16(ipAddr.Port)
		if err == nil {
			if guessIp == "127.0.0.1" || guessIp == "::" {
				guessIp = serverConfig.SipIP
				guessPort = serverConfig.SipPort
			}
		}
		d = &Device{
			ID:           id,
			RegisterTime: time.Now(),
			UpdateTime:   time.Now(),
			Status:       string(sip.REGISTER),
			addr:         deviceAddr,
			tx:           tx,
			Addr:         req.Source(),
			channelMap:   make(map[string]*Channel),
			config:       serverConfig,
			Transport:    strings.ToUpper(req.Transport()),
			SipIP:        guessIp,
			SipPort:      guessPort,
			MediaIP:      serverConfig.MediaIP,
			MediaPort:    serverConfig.MediaPort,
		}
		Devices.Store(id, d)
		logrus.Debug("StoreDevice ", d)
		go d.Catalog()
	}
}

func run() {
	ipAddr, err := net.ResolveUDPAddr("", config.ListenAddr)
	if err != nil {
		log.Fatal(err)
	}
	for _, id := range config.Ignore {
		serverConfig.Ignores[id] = struct{}{}
	}
	if config.TCP {
		serverConfig.SipNetwork = "tcp"
		serverConfig.MediaNetwork = "tcp"
	} else {
		serverConfig.SipNetwork = "udp"
		serverConfig.MediaNetwork = "udp"
	}
	if config.SipIP == "127.0.0.1" || config.SipIP == "0.0.0.0" {
		serverConfig.SipIP = ipAddr.IP.String()
	} else {
		serverConfig.SipIP = config.SipIP
	}
	serverConfig.SipPort = config.SipPort
	serverConfig.Serial = config.Serial
	serverConfig.Realm = config.Realm
	serverConfig.Username = config.Username
	serverConfig.Password = config.Password
	serverConfig.AckTimeout = 10
	serverConfig.MediaIP = config.MediaIP
	serverConfig.MediaPort = config.MediaPort
	if config.MediaIP == "0.0.0.0" {
		serverConfig.MediaIP = config.SipIP
	}

	serverConfig.RegisterValidity = config.Expires
	serverConfig.RegisterInterval = 60
	serverConfig.HeartbeatInterval = 60
	serverConfig.HeartbeatRetry = 3
	serverConfig.AudioEnable = true
	serverConfig.WaitKeyFrame = true
	serverConfig.MediaIdleTimeout = 30
	serverConfig.RemoveBanInterval = config.RemoveBanInterval
	serverConfig.UdpCacheSize = config.UdpCacheSize
	serverConfig.LogVerbose = config.LogVerbose
	serverConfig.MediaPortMin = config.MediaPort
	serverConfig.MediaPortMax = config.MediaPort + config.TCPMediaPortNum - 1

	http.HandleFunc("/api/gb28181/query/records", func(w http.ResponseWriter, r *http.Request) {
		CORS(w, r)
		id := r.URL.Query().Get("id")
		channel := r.URL.Query().Get("channel")
		startTime := r.URL.Query().Get("startTime")
		endTime := r.URL.Query().Get("endTime")
		if c := FindChannel(id, channel); c != nil {
			w.WriteHeader(c.QueryRecord(startTime, endTime))
		} else {
			w.WriteHeader(404)
		}
	})
	http.HandleFunc("/api/gb28181/list", func(w http.ResponseWriter, r *http.Request) {
		CORS(w, r)
		sse := NewSSE(w, r.Context())
		for {
			var list []*Device
			Devices.Range(func(key, value interface{}) bool {
				device := value.(*Device)
				if time.Since(device.UpdateTime) > time.Duration(serverConfig.RegisterValidity)*time.Second {
					Devices.Delete(key)
				} else {
					list = append(list, device)
				}
				return true
			})
			sse.WriteJSON(list)
			select {
			case <-time.After(time.Second * 5):
			case <-sse.Done():
				return
			}
		}
	})
	http.HandleFunc("/api/gb28181/control", func(w http.ResponseWriter, r *http.Request) {
		CORS(w, r)
		id := r.URL.Query().Get("id")
		channel := r.URL.Query().Get("channel")
		ptzcmd := r.URL.Query().Get("ptzcmd")
		if c := FindChannel(id, channel); c != nil {
			w.WriteHeader(c.Control(ptzcmd))
		} else {
			w.WriteHeader(404)
		}
	})
	http.HandleFunc("/api/gb28181/invite", func(w http.ResponseWriter, r *http.Request) {
		CORS(w, r)
		query := r.URL.Query()
		id := query.Get("id")
		channel := r.URL.Query().Get("channel")
		startTime := query.Get("startTime")
		endTime := query.Get("endTime")
		if c := FindChannel(id, channel); c != nil {
			if startTime == "" && c.LivePublisher != nil {
				w.WriteHeader(304) //直播流已存在
			} else {
				w.WriteHeader(c.Invite(startTime, endTime))
			}
		} else {
			w.WriteHeader(404)
		}
	})
	http.HandleFunc("/api/gb28181/bye", func(w http.ResponseWriter, r *http.Request) {
		CORS(w, r)
		id := r.URL.Query().Get("id")
		channel := r.URL.Query().Get("channel")
		live := r.URL.Query().Get("live")
		if c := FindChannel(id, channel); c != nil {
			w.WriteHeader(c.Bye(live != "false"))
		} else {
			w.WriteHeader(404)
		}
	})
	http.HandleFunc("/api/gb28181/position", func(w http.ResponseWriter, r *http.Request) {
		CORS(w, r)
		query := r.URL.Query()
		//设备id
		id := query.Get("id")
		//订阅周期(单位：秒)
		expires := query.Get("expires")
		//订阅间隔（单位：秒）
		interval := query.Get("interval")

		expiresInt, _ := strconv.Atoi(expires)
		intervalInt, _ := strconv.Atoi(interval)

		if v, ok := Devices.Load(id); ok {
			d := v.(*Device)
			w.WriteHeader(d.MobilePositionSubscribe(id, expiresInt, intervalInt))
		} else {
			w.WriteHeader(404)
		}
	})

	serverConfig.startServer()
}
