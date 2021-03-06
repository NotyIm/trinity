package client

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/orcaman/concurrent-map"

	"github.com/notyim/gaia"
	"github.com/notyim/gaia/dao"
	"github.com/notyim/gaia/me"
	"github.com/notyim/gaia/scanner"
)

type AgentInfo struct {
	Name    string
	Version string
	IPinfo  *me.IPinfo
}

type Agent struct {
	AgentInfo      *AgentInfo
	Checks         *cmap.ConcurrentMap
	gaiaAddress    *url.URL
	conn           *websocket.Conn
	isReconnecting bool

	// Gorilla websocket doesn't support concurently write, therefore this mutex
	mu          sync.Mutex
	config      *Config
	ScannerPool *scanner.Pool
}

func New() *Agent {
	hostname, _ := os.Hostname()
	ipinfo, err := me.Fetch()
	if err != nil {
		log.Fatal("Failt to fetch GEO IP")
	}

	agentInfo := AgentInfo{
		Name:    fmt.Sprintf("%s#%d", hostname, os.Getpid()),
		Version: gaia.Version("Client"),
		IPinfo:  ipinfo,
	}
	checks := cmap.New()
	a := Agent{
		AgentInfo:      &agentInfo,
		Checks:         &checks,
		config:         LoadConfig(),
		isReconnecting: false,
	}
	a.ScannerPool = scanner.NewPool(&a, a.config.WorkerPool)

	return &a
}

func (a *Agent) Run() {
	gaia.SetupErrorLog()
	a.Connect()
	a.SyncState()
}

func (a *Agent) Connect() {
	scheme := "wss"
	if a.config.GaiaAddr == "localhost:28300" {
		scheme = "ws"
	}
	params := url.Values{}
	params.Add("version", a.AgentInfo.Version)
	params.Add("region", a.AgentInfo.IPinfo.Region)
	params.Add("apikey", a.config.GaiaApiKey)

	u := url.URL{Scheme: scheme, Host: a.config.GaiaAddr, Path: "/ws/" + a.AgentInfo.Name, RawQuery: params.Encode()}
	a.gaiaAddress = &u
	var err error
	log.Println("Connect to", a.gaiaAddress.String())
	a.conn, _, err = websocket.DefaultDialer.Dial(a.gaiaAddress.String(), nil)

	if err != nil {
		log.Fatal("dial:", a.config.GaiaAddr, err)
	}
}

func (a *Agent) IPAddress() string {
	return a.AgentInfo.IPinfo.IP
}

func (a *Agent) Region() string {
	return a.AgentInfo.IPinfo.Region + ", " + a.AgentInfo.IPinfo.Country
}

func (a *Agent) ReconnectWithRetry(cause error) {
	// TODO: is a mutex safer/better
	if a.isReconnecting {
		log.Println("Don't try to reconnect because one is in-progeess")
		return
	}
	defer func() {
		a.isReconnecting = false
	}()
	a.isReconnecting = true
	log.Println("Got an error when writing to gaia", cause, "Will reconnect until succesful")

	for {
		var err error
		a.conn, _, err = websocket.DefaultDialer.Dial(a.gaiaAddress.String(), nil)
		if err == nil {
			log.Println("Reconnect succesfully at", time.Now())
			break
		}
		log.Println("Still getting error", err, "Wainting for 5 second before retrying")
		time.Sleep(5 * time.Second)
	}
}

func (a *Agent) PushToServer(payload []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.conn != nil {
		err := a.conn.WriteMessage(websocket.TextMessage, payload)
		if err != nil {
			go a.ReconnectWithRetry(err)
		}

		return err
	}

	log.Println("Gaia connection is nil. Probably we haven't finishing connecting to gaia or gaia is down")

	return nil
}

func (a *Agent) ProcessServerCommand(evt *gaia.GenericEvent) {
	switch evt.EventType {
	case gaia.EventTypeCheckInsert:
		a.Checks.Set(evt.EventCheckInsert.Check.ID.Hex(), evt.EventCheckInsert.Check)
	case gaia.EventTypeCheckReplace:
		a.Checks.Set(evt.EventCheckReplace.Check.ID.Hex(), evt.EventCheckReplace.Check)
	case gaia.EventTypeCheckDelete:
		a.Checks.Remove(evt.EventCheckDelete.ID.Hex())
	case gaia.EventTypeRunCheck:
		log.Println("Run check", evt.EventRunCheck)

		val, ok := a.Checks.Get(evt.EventRunCheck.ID)
		if !ok {
			log.Println("Server request check but it didn't exist on client state", evt.EventRunCheck)
			return
		}
		check := val.(*dao.Check)
		log.Println("Start to check", check.URI)
		a.ScannerPool.Queue <- check
	default:
		log.Println("Receive an unknow message", evt)
	}
}

func (a *Agent) SyncState() {
	defer a.conn.Close()
	done := make(chan struct{})

	go func() {
		for {
			_, message, err := a.conn.ReadMessage()
			if err != nil {
				log.Println("Error when recieving message from server", err)
				// Retrying server connection
				a.ReconnectWithRetry(err)
				continue
			}
			log.Printf("Message from server %s", message)

			var evt gaia.GenericEvent
			if err = evt.UnmarshalJSON(message); err != nil {
				continue
			}
			go a.ProcessServerCommand(&evt)
		}
	}()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	pingCmd := gaia.NewEventPing()
	pingPayload, _ := json.Marshal(pingCmd)
	for {
		select {
		case <-done:
			return
		case t := <-ticker.C:
			// TODO: Do its own client heal check and stop the whole app if needed so gaia-agent can be restarted with
			// systemd
			log.Println("Ticker at", t)
			a.PushToServer(pingPayload)
		case <-interrupt:
			log.Println("interrupt")

			// Cleanly close the connection by sending a close message and then
			// waiting (with timeout) for the server to close the connection.
			if a.conn != nil {
				err := a.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				if err != nil {
					log.Println("write close:", err)
				}
			}
			a.ScannerPool.Close()

			select {
			case <-done:
			// 30 seconds to force close
			case <-time.After(3 * time.Second):
			}
			return
		}
	}
}
