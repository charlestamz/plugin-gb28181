package gb28181

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Monibuca/engine/v3"
	"github.com/Monibuca/plugin-gb28181/v3/utils"
	. "github.com/Monibuca/utils/v3"
	// . "github.com/logrusorgru/aurora"
	"github.com/ghettovoice/gosip/sip"
)

const TIME_LAYOUT = "2006-01-02T15:04:05"

// Record 录像
type Record struct {
	//channel   *Channel
	DeviceID  string
	Name      string
	FilePath  string
	Address   string
	StartTime string
	EndTime   string
	Secrecy   int
	Type      string
}

func (r *Record) GetPublishStreamPath() string {
	return fmt.Sprintf("%s/%s", r.DeviceID, r.StartTime)
}

var (
	Devices             sync.Map
	DeviceNonce         sync.Map //保存nonce防止设备伪造
	DeviceRegisterCount sync.Map //设备注册次数
)

type Device struct {
	//*transaction.Core `json:"-"`
	config          *GB28181Config
	ID              string
	Name            string
	Manufacturer    string
	Model           string
	Owner           string
	RegisterTime    time.Time
	UpdateTime      time.Time
	LastKeepaliveAt time.Time
	Status          string
	Channels        []*Channel
	sn              int
	addr            sip.Address
	tx              *sip.ServerTransaction
	Addr            string //Compat for UI
	SipIP           string //对设备暴露的IP
	SipPort         uint16
	MediaIP         string //对设备暴露的IP
	MediaPort       uint16

	Transport    string //每个设备都有自己的传输方式
	channelMap   map[string]*Channel
	channelMutex sync.RWMutex
	subscriber   struct {
		CallID  string
		Timeout time.Time
	}
}

func (d *Device) addChannel(channel *Channel) {
	for _, c := range d.Channels {
		if c.DeviceID == channel.DeviceID {
			return
		}
	}
	d.Channels = append(d.Channels, channel)
}

func (d *Device) CheckSubStream() {
	d.channelMutex.Lock()
	defer d.channelMutex.Unlock()
	for _, c := range d.Channels {
		if s := engine.FindStream("sub/" + c.DeviceID); s != nil {
			c.LiveSubSP = s.StreamPath
		} else {
			c.LiveSubSP = ""
		}
	}
}
func (d *Device) UpdateChannels(list []*Channel) {
	d.channelMutex.Lock()
	defer d.channelMutex.Unlock()
	for _, c := range list {
		if _, ok := d.config.Ignores[c.DeviceID]; ok {
			continue
		}
		if c.ParentID != "" {
			path := strings.Split(c.ParentID, "/")
			parentId := path[len(path)-1]
			if parent, ok := d.channelMap[parentId]; ok {
				if c.DeviceID != parentId {
					parent.Children = append(parent.Children, c)
				}
			} else {
				d.addChannel(c)
			}
		} else {
			d.addChannel(c)
		}
		if old, ok := d.channelMap[c.DeviceID]; ok {
			c.ChannelEx = old.ChannelEx
			if d.config.PreFetchRecord {
				n := time.Now()
				n = time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.Local)
				if len(c.Records) == 0 || (n.Format(TIME_LAYOUT) == c.RecordStartTime &&
					n.Add(time.Hour*24-time.Second).Format(TIME_LAYOUT) == c.RecordEndTime) {
					go c.QueryRecord(n.Format(TIME_LAYOUT), n.Add(time.Hour*24-time.Second).Format(TIME_LAYOUT))
				}
			}
			if serverConfig.AutoInvite &&
				(c.LivePublisher == nil || (c.LivePublisher.VideoTracks.Size == 0 && c.LivePublisher.AudioTracks.Size == 0)) {
				c.Invite("", "")
			}

		} else {
			c.ChannelEx = &ChannelEx{
				device: d,
			}
			if d.config.AutoInvite {
				c.Invite("", "")
			}
		}
		if s := engine.FindStream("sub/" + c.DeviceID); s != nil {
			c.LiveSubSP = s.StreamPath
		} else {
			c.LiveSubSP = ""
		}
		d.channelMap[c.DeviceID] = c
	}
}
func (d *Device) UpdateRecord(channelId string, list []*Record) {
	d.channelMutex.RLock()
	if c, ok := d.channelMap[channelId]; ok {
		c.Records = append(c.Records, list...)
	}
	d.channelMutex.RUnlock()
}

func (d *Device) CreateRequest(Method sip.RequestMethod) (req sip.Request) {
	d.sn++

	callId := sip.CallID(utils.RandNumString(10))
	userAgent := sip.UserAgentHeader("Monibuca")
	cseq := sip.CSeq{
		SeqNo:      uint32(d.sn),
		MethodName: Method,
	}
	port := sip.Port(d.SipPort)
	serverAddr := sip.Address{
		//DisplayName: sip.String{Str: d.serverConfig.Serial},
		Uri: &sip.SipUri{
			FUser: sip.String{Str: d.config.Serial},
			FHost: d.SipIP,
			FPort: &port,
		},
		Params: sip.NewParams().Add("tag", sip.String{Str: utils.RandNumString(9)}),
	}
	req = sip.NewRequest(
		"",
		Method,
		d.addr.Uri,
		"SIP/2.0",
		[]sip.Header{
			serverAddr.AsFromHeader(),
			d.addr.AsToHeader(),
			&callId,
			&userAgent,
			&cseq,
			serverAddr.AsContactHeader(),
		},
		"",
		nil,
	)

	req.SetTransport(d.Transport)
	req.SetDestination(d.Addr)
	//fmt.Printf("构建请求参数:%s", *&req)
	// requestMsg.DestAdd, err2 = d.ResolveAddress(requestMsg)
	// if err2 != nil {
	// 	return nil
	// }
	//intranet ip , let's resolve it with public ip
	// var deviceIp, deviceSourceIP net.IP
	// switch addr := requestMsg.DestAdd.(type) {
	// case *net.UDPAddr:
	// 	deviceIp = addr.IP
	// case *net.TCPAddr:
	// 	deviceIp = addr.IP
	// }

	// switch addr2 := d.SourceAddr.(type) {
	// case *net.UDPAddr:
	// 	deviceSourceIP = addr2.IP
	// case *net.TCPAddr:
	// 	deviceSourceIP = addr2.IP
	// }
	// if deviceIp.IsPrivate() && !deviceSourceIP.IsPrivate() {
	// 	requestMsg.DestAdd = d.SourceAddr
	// }
	return
}

func (d *Device) Subscribe() int {
	request := d.CreateRequest(sip.SUBSCRIBE)
	if d.subscriber.CallID != "" {
		callId := sip.CallID(utils.RandNumString(10))
		request.AppendHeader(&callId)
	}
	expires := sip.Expires(3600)
	d.subscriber.Timeout = time.Now().Add(time.Second * time.Duration(expires))
	contentType := sip.ContentType("Application/MANSCDP+xml")
	request.AppendHeader(&contentType)
	request.AppendHeader(&expires)

	request.SetBody(BuildCatalogXML(d.sn, d.ID), true)

	response, err := d.SipRequestForResponse(request)
	if err == nil && response != nil {
		if response.StatusCode() == 200 {
			callId, _ := request.CallID()
			d.subscriber.CallID = string(*callId)
		} else {
			d.subscriber.CallID = ""
		}
		return int(response.StatusCode())
	}
	return http.StatusRequestTimeout
}

func (d *Device) Catalog() int {
	request := d.CreateRequest(sip.MESSAGE)
	expires := sip.Expires(3600)
	d.subscriber.Timeout = time.Now().Add(time.Second * time.Duration(expires))
	contentType := sip.ContentType("Application/MANSCDP+xml")

	request.AppendHeader(&contentType)
	request.AppendHeader(&expires)
	request.SetBody(BuildCatalogXML(d.sn, d.ID), true)
	// 输出Sip请求设备通道信息信令
	resp, err := d.SipRequestForResponse(request)
	if err == nil && resp != nil {
		return int(resp.StatusCode())
	}
	return http.StatusRequestTimeout
}

func (d *Device) QueryDeviceInfo() {
	for i := time.Duration(5); i < 100; i++ {

		Printf(fmt.Sprintf("QueryDeviceInfo:%s ipaddr:%s", d.ID, d.Addr))
		time.Sleep(time.Second * i)
		request := d.CreateRequest(sip.MESSAGE)
		contentType := sip.ContentType("Application/MANSCDP+xml")
		request.AppendHeader(&contentType)
		request.SetBody(BuildDeviceInfoXML(d.sn, d.ID), true)

		response, err := d.SipRequestForResponse(request)
		if err != nil {
			Printf("QueryDeviceInfo device %s send Catalog response Error: %s\n", d.ID, err)
		}
		if response != nil {
			via, _ := response.ViaHop()
			if via != nil {
				d.SipIP = via.Host
			}
			if d.MediaIP == "127.0.0.1" || d.MediaIP == "::" || d.MediaIP == "0.0.0.0" {
				d.MediaIP = d.SipIP
				d.MediaPort = serverConfig.MediaPort
			}
			if response.StatusCode() != 200 {
				Printf("device %s send Catalog : %d\n", d.ID, response.StatusCode())
			} else {
				d.Subscribe()
				break
			}
		}
	}
}

func (d *Device) SipRequestForResponse(request sip.Request) (sip.Response, error) {
	return (*GetSipServer(d.Transport)).RequestWithContext(context.Background(), request)
}

// MobilePositionSubscribe 移动位置订阅
func (d *Device) MobilePositionSubscribe(id string, expires int, interval int) (code int) {
	mobilePosition := d.CreateRequest(sip.SUBSCRIBE)
	if d.subscriber.CallID != "" {
		callId := sip.CallID(utils.RandNumString(10))
		mobilePosition.ReplaceHeaders(callId.Name(), []sip.Header{&callId})
	}
	expiresHeader := sip.Expires(expires)
	d.subscriber.Timeout = time.Now().Add(time.Second * time.Duration(expires))
	contentType := sip.ContentType("Application/MANSCDP+xml")
	mobilePosition.AppendHeader(&contentType)
	mobilePosition.AppendHeader(&expiresHeader)

	mobilePosition.SetBody(BuildDevicePositionXML(d.sn, id, interval), true)

	response, err := d.SipRequestForResponse(mobilePosition)
	if err == nil && response != nil {
		if response.StatusCode() == 200 {
			callId, _ := mobilePosition.CallID()
			d.subscriber.CallID = callId.String()
		} else {
			d.subscriber.CallID = ""
		}
		return int(response.StatusCode())
	}
	return http.StatusRequestTimeout
}

// UpdateChannelPosition 更新通道GPS坐标
func (d *Device) UpdateChannelPosition(channelId string, gpsTime string, lng string, lat string) {
	if c, ok := d.channelMap[channelId]; ok {
		c.ChannelEx.GpsTime, _ = time.ParseInLocation("2006-01-02 15:04:05", gpsTime, time.Local)
		c.ChannelEx.Longitude = lng
		c.ChannelEx.Latitude = lat
		log.Printf("更新通道[%s]坐标成功\n", c.Name)
	} else {
		log.Printf("更新失败，未找到通道[%s]\n", channelId)
	}
}

// UpdateChannelStatus 目录订阅消息处理：新增/移除/更新通道或者更改通道状态
func (d *Device) UpdateChannelStatus(deviceList []*notifyMessage) {
	for _, v := range deviceList {
		switch v.Event {
		case "ON":
			log.Println("收到通道上线通知")
			d.channelOnline(v.DeviceID)
		case "OFF":
			log.Println("收到通道离线通知")
			d.channelOffline(v.DeviceID)
		case "VLOST":
			log.Println("收到通道视频丢失通知")
			d.channelOffline(v.DeviceID)
		case "DEFECT":
			log.Println("收到通道故障通知")
			d.channelOffline(v.DeviceID)
		case "ADD":
			log.Println("收到通道新增通知")
			channel := Channel{
				DeviceID:     v.DeviceID,
				ParentID:     v.ParentID,
				Name:         v.Name,
				Manufacturer: v.Manufacturer,
				Model:        v.Model,
				Owner:        v.Owner,
				CivilCode:    v.CivilCode,
				Address:      v.Address,
				Parental:     v.Parental,
				SafetyWay:    v.SafetyWay,
				RegisterWay:  v.RegisterWay,
				Secrecy:      v.Secrecy,
				Status:       v.Status,
			}
			d.addChannel(&channel)
		case "DEL":
			//删除
			log.Println("收到通道删除通知")
			delete(d.channelMap, v.DeviceID)
		case "UPDATE":
			fmt.Println("收到通道更新通知")
			// 更新通道
			channel := &Channel{
				DeviceID:     v.DeviceID,
				ParentID:     v.ParentID,
				Name:         v.Name,
				Manufacturer: v.Manufacturer,
				Model:        v.Model,
				Owner:        v.Owner,
				CivilCode:    v.CivilCode,
				Address:      v.Address,
				Parental:     v.Parental,
				SafetyWay:    v.SafetyWay,
				RegisterWay:  v.RegisterWay,
				Secrecy:      v.Secrecy,
				Status:       v.Status,
			}
			channels := []*Channel{channel}
			d.UpdateChannels(channels)
		}
	}
}

func (d *Device) channelOnline(DeviceID string) {
	if c, ok := d.channelMap[DeviceID]; ok {
		c.Status = "ON"
		log.Printf("通道[%s]在线\n", c.Name)
	} else {
		log.Printf("更新通道[%s]状态失败，未找到\n", DeviceID)
	}
}

func (d *Device) channelOffline(DeviceID string) {
	if c, ok := d.channelMap[DeviceID]; ok {
		c.Status = "OFF"
		log.Printf("通道[%s]离线\n", c.Name)
	} else {
		log.Printf("更新通道[%s]状态失败，未找到\n", DeviceID)
	}
}
