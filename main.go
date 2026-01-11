



package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/buger/jsonparser"
	"github.com/valyala/fasthttp"

	"net"
	"runtime"
	"strings"
    "github.com/fsnotify/fsnotify"
	"github.com/bytedance/sonic"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)






const (
	gatewayURL = "wss://gateway-us-east1-c.discord.gg"

	opHello     = 10
	opIdentify  = 2
	opHeartbeat = 1
	opDispatch  = 0
)

var lastGuildFileCheck int64
var lastGuildFileModTime time.Time

var staticHeaders = func() *fasthttp.RequestHeader {
	h := &fasthttp.RequestHeader{}
	h.Set("Accept", "*/*")
	h.Set("Content-Type", "application/json")
	h.Set("Connection", "keep-alive")
	h.Set("X-Super-Properties", "eyJvcyI6ImlPUyIsImJyb3dzZXIiOiJTYWZhcmkiLCJkZXZpY2UiOiJpUGhvbmUgMTMiLCJzeXN0ZW1fbG9jYWxlIjoiZW4tVVMiLCJicm93c2VyX3VzZXJfYWdlbnQiOiJNb3ppbGxhLzUuMCAoaVBob25lOyBDUFUgaVBhb25lIE9TIDE3XzAgbGlrZSBNYWMgT1MgWCkgQXBwbGVXZWJLaXQvNjA1LjEuMTUgKEtIVE1MLCBsaWtlIEdlY2tvKSBWZXJzaW9uLzE3LjAgTW9iaWxlLzE1RTE0OCBTYWZhcmkvNjA1LjEiLCJicm93c2VyX3ZlcnNpb24iOiIxNy4wIiwib3NfdmVyc2lvbiI6IjE3LjAiLCJyZWZlcnJlciI6Imh0dHBzOi8vd3d3Lmdvb2dsZS5jb20vIiwicmVmZXJyaW5nX2RvbWFpbiI6Ind3dy5nb29nbGUuY29tIiwic2VhcmNoX2VuZ2luZSI6Ikdvb2dsZSIsInJlZmVycmVyX2N1cnJlbnQiOiIiLCJyZWZlcnJpbmdfZG9tYWluX2N1cnJlbnQiOiIiLCJyZWxlYXNlX2NoYW5uZWwiOiJzdGFibGUiLCJjbGllZW50X2J1aWxkX251bWJlciI6MzQyOTY4LCJjbGllZW50X2V2ZW50X3NvdXJjZSI6bnVsbH0K")
	return h
}()

var payloadPool = sync.Pool{
	New: func() interface{} { return &Payload{} },
}


var (
    monitorClient = &fasthttp.Client{ 
        MaxConnsPerHost:           64,
        MaxIdleConnDuration:       20 * time.Second,
        MaxConnDuration:           45 * time.Second,
        ReadTimeout:               4 * time.Second,
        WriteTimeout:              4 * time.Second,
        MaxResponseBodySize:       512 * 1024,
    }

    claimClient = &fasthttp.Client{ 
        MaxConnsPerHost:           200,
        MaxIdleConnDuration:       10 * time.Second,
        MaxConnDuration:           25 * time.Second,
        ReadTimeout:               1500 * time.Millisecond,
        WriteTimeout:              1500 * time.Millisecond,
        MaxResponseBodySize:       256 * 1024,
        NoDefaultUserAgentHeader:  true,
    }
)


type Config struct {
    MonitorToken  string   `json:"monitorToken"`
    ClaimToken    string   `json:"claimToken"`
    Hook          string   `json:"hook"`
    MfaPassword   string   `json:"mfa_password"`
    ParallelClaim bool     `json:"parallelclaim"`
    LeaveAfterClaim bool   `json:"leave_after_claim"`
	
}

var (
    monitorToken string
    claimToken   string
    claimGuildIDs []string
    HOOK         string
    currentGuildIndex atomic.Int32
    mfaPassword string
    parallelClaim bool = true
    leaveAfterClaim bool = true
)

type Sniper struct {
    guilds        map[string]*Guild
    conn          net.Conn
    heartbeatInt  time.Duration
    seq           int
    ctx           context.Context
    cancel        context.CancelFunc
    mfaTOKEN      string
    guildMutex    sync.Mutex
}
type Guild struct {
    ID               string  `json:"id"`
    Name             string  `json:"name"`
    VanityURLCode    *string `json:"vanity_url_code"`
    ClaimedVanityURL string
}



type ReadyData struct {
    User struct {
        Username string `json:"username"`
        ID       string `json:"id"`
    } `json:"user"`
    Guilds []Guild `json:"guilds"`
}
type Payload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  int             `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

type HelloData struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}





func NewSniper() *Sniper {
	ctx, cancel := context.WithCancel(context.Background())
	return &Sniper{
		guilds: make(map[string]*Guild),
		ctx:    ctx,
		cancel: cancel,
	}
}

func (s *Sniper) Connect() error {
	dialer := ws.Dialer{
		ReadBufferSize:  65536,
		WriteBufferSize: 65536,
		NetDial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := &net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}
			conn, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			if tcpConn, ok := conn.(*net.TCPConn); ok {
				tcpConn.SetNoDelay(true)
				tcpConn.SetKeepAlive(true)
				tcpConn.SetKeepAlivePeriod(30 * time.Second)
			}
			return conn, nil
		},
	}
	conn, _, _, err := dialer.Dial(s.ctx, gatewayURL)
	if err != nil {
		return fmt.Errorf("dial error: %w", err)
	}
	s.conn = conn
	return nil
}

func (s *Sniper) send(payload interface{}) error {
	data, err := sonic.Marshal(payload) 
	if err != nil {
		return err
	}
	return wsutil.WriteClientMessage(s.conn, ws.OpText, data)
}

func (s *Sniper) receive() ([]byte, time.Duration, time.Duration, error) {
	startRead := time.Now()
	msg, err := wsutil.ReadServerMessage(s.conn, nil)
	readLatency := time.Since(startRead)
	if err != nil {
		return nil, 0, 0, err
	}



	data := msg[0].Payload
	return data, readLatency, 0, nil
}

func (s *Sniper) identify() error {
	return s.send(map[string]interface{}{
		"op": opIdentify,
		"d": map[string]interface{}{
			"token":   monitorToken,
			"intents": 201,
			"properties": map[string]string{
				"os":      "IOS",
				"browser": "Safari",
				"device":  "apple",
			},
			"compress": false,
		},
	})
}

func (s *Sniper) heartbeat() {
	ticker := time.NewTicker(s.heartbeatInt)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := s.send(map[string]interface{}{
				"op": opHeartbeat,
				"d":  s.seq,
			}); err != nil {
				log.Printf("Heartbeat error: %v", err)
				return
			}
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *Sniper) handleGuildUpdate(p *Payload) {


	var gu Guild
	if err := sonic.Unmarshal(p.D, &gu); err != nil {
		log.Printf("Error unmarshalling GUILD_UPDATE: %v", err)
		return
	}


	newVanity := ""
	if gu.VanityURLCode != nil {
		newVanity = *gu.VanityURLCode
	}
	guildID := gu.ID

	s.guildMutex.Lock()
	existingGuild, exists := s.guilds[guildID]
	if !exists {
		s.guilds[guildID] = &Guild{ID: guildID, Name: gu.Name, VanityURLCode: gu.VanityURLCode}
		s.guildMutex.Unlock()
		log.Printf("Added new guild %s (no vanity tracking)", guildID)
		return
	}

	oldVanity := ""
	if existingGuild.VanityURLCode != nil {
		oldVanity = *existingGuild.VanityURLCode
	}

	if oldVanity != "" && (newVanity == "" || newVanity != oldVanity) {
		detectionTime := time.Now().UnixMilli()
		go claim_vanity(oldVanity, s.mfaTOKEN, detectionTime, guildID, "GUILD_UPDATE")
	}

	existingGuild.VanityURLCode = gu.VanityURLCode
	existingGuild.Name = gu.Name
	s.guildMutex.Unlock()


}
func (s *Sniper) handleGuildCreate(p *Payload) {
	processingStart := time.Now()

	var gu Guild
	if err := sonic.Unmarshal(p.D, &gu); err != nil {
		log.Printf("Error unmarshalling GUILD_CREATE: %v", err)
		return
	}

	guildID := gu.ID
	newVanity := ""
	if gu.VanityURLCode != nil {
		newVanity = *gu.VanityURLCode
	}

	s.guildMutex.Lock()
	if _, exists := s.guilds[guildID]; !exists {
		s.guilds[guildID] = &Guild{ID: guildID, Name: gu.Name, VanityURLCode: gu.VanityURLCode}
		log.Printf("added a new guild, will be sniping dat too now : %s (vanity: %s)", guildID, newVanity)
	}
	s.guildMutex.Unlock()

	processingEnd := time.Now()
	log.Printf("processed GUILD_CREATE in %v", processingEnd.Sub(processingStart))
}

func (s *Sniper) handleReady(p *Payload) {
	var readyData ReadyData
	if err := sonic.Unmarshal(p.D, &readyData); err != nil {
		log.Printf("failed to parse READY event: %v", err)
		return
	}


	s.guildMutex.Lock()
	for _, guild := range readyData.Guilds {
		if guild.VanityURLCode != nil && *guild.VanityURLCode != "" {
			fmt.Printf("%s\n", *guild.VanityURLCode)
		}
		s.guilds[guild.ID] = &guild
	}
	s.guildMutex.Unlock()
}

func (s *Sniper) handleGuildDelete(p *Payload) {
	var guild Guild
	if err := sonic.Unmarshal(p.D, &guild); err != nil {
		return
	}

	s.guildMutex.Lock()
	existingGuild, exists := s.guilds[guild.ID]
	if exists {
		oldVanity := ""
		if existingGuild.VanityURLCode != nil {
			oldVanity = *existingGuild.VanityURLCode
		}
		if oldVanity != "" {
			detectionTime := time.Now().UnixMilli()
			go claim_vanity(oldVanity, s.mfaTOKEN, detectionTime, guild.ID, "GUILD_DELETE")

		}
		delete(s.guilds, guild.ID)
	}
	s.guildMutex.Unlock()
}

func (s *Sniper) Run() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := s.Connect(); err != nil {
		return err
	}
	defer s.conn.Close()


	rawPayload, _, _, err := s.receive()
	if err != nil {
		return fmt.Errorf("hello receive error: %w", err)
	}

	p := payloadPool.Get().(*Payload)

	if err := sonic.Unmarshal(rawPayload, p); err != nil {
		payloadPool.Put(p)
		return fmt.Errorf("hello unmarshal error: %w", err)
	}


	if p.Op != opHello {
		payloadPool.Put(p)
		return fmt.Errorf("expected HELLO, got op %d", p.Op)
	}

	var hello HelloData
	if err := sonic.Unmarshal(p.D, &hello); err != nil {
		payloadPool.Put(p)
		return err
	}
	payloadPool.Put(p)

	s.heartbeatInt = time.Duration(hello.HeartbeatInterval) * time.Millisecond
	go s.heartbeat()
	if err := s.identify(); err != nil {
		return err
	}

	fmt.Println(cyan + "watching servers ->\n" + reset)


	SetTitle("goclaimer  V3 github.com/00nx")


	for {
		select {
		case <-s.ctx.Done():
			return nil
		default:
		}

		rawPayload, _, _, err := s.receive()
		if err != nil {
			log.Printf("Receive error: %v. Reconnecting...", err)
			return err
		}

		p := payloadPool.Get().(*Payload)
		if err := sonic.Unmarshal(rawPayload, p); err != nil {
			log.Printf("Error unmarshalling payload: %v", err)
			payloadPool.Put(p)
			continue
		}

		if p.S > s.seq {
			s.seq = p.S
		}

		if p.Op == opDispatch {
			switch p.T {
			case "GUILD_UPDATE":
				s.handleGuildUpdate(p)
			case "READY":
				s.handleReady(p)
			case "GUILD_DELETE":
				s.handleGuildDelete(p)
			case "GUILD_CREATE":
				s.handleGuildCreate(p)
			}
		}

		payloadPool.Put(p)
	}
}

func (s *Sniper) Close() {
	s.cancel()
	if s.conn != nil {
		s.conn.Close()
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Microsecond {
		return fmt.Sprintf("%dns", d.Nanoseconds())
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%.3fµs", float64(d.Nanoseconds())/1000.0)
	}
	if d < time.Second {
		return fmt.Sprintf("%.3fms", float64(d.Microseconds())/1000.0)
	}
	return fmt.Sprintf("%.3fs", d.Seconds())
}

func genMFA(s *Sniper) error {
    vanityAPI := "https://canary.discord.com/api/v9/guilds/929176740968923146/vanity-url"
    finishAPI := "https://canary.discord.com/api/v9/mfa/finish"
    url := vanityAPI

    req := fasthttp.AcquireRequest()
    defer fasthttp.ReleaseRequest(req)

    req.SetRequestURI(url)
    req.Header.SetMethod("PATCH")
    req.Header.Set("Host", "discord.com")
    req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/605.1")
    req.Header.Set("Accept", "*/*")
    req.Header.Set("Accept-Language", "en-US,en;q=0.5")
    req.Header.Set("Accept-Encoding", "utf8")
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Authorization", claimToken)
    req.Header.Set("X-Super-Properties", "eyJvcyI6ImlPUyIsImJyb3dzZXIiOiJTYWZhcmkiLCJkZXZpY2UiOiJpUGhvbmUgMTMiLCJzeXN0ZW1fbG9jYWxlIjoiZW4tVVMiLCJicm93c2VyX3VzZXJfYWdlbnQiOiJNb3ppbGxhLzUuMCAoaVBob25lOyBDUFUgaVBob25lIE9TIDE3XzAgbGlrZSBNYWMgT1MgWCkgQXBwbGVXZWJLaXQvNjA1LjEuMTUgKEtIVE1MLCBsaWtlIEdlY2tvKSBWZXJzaW9uLzE3LjAgTW9iaWxlLzE1RTE0OCBTYWZhcmkvNjA1LjEiLCJicm93c2VyX3ZlcnNpb24iOiIxNy4wIiwib3NfdmVyc2lvbiI6IjE3LjAiLCJyZWZlcnJlciI6Imh0dHBzOi8vd3d3Lmdvb2dsZS5jb20vIiwicmVmZXJyaW5nX2RvbWFpbiI6Ind3dy5nb29nbGUuY29tIiwic2VhcmNoX2VuZ2luZSI6Ikdvb2dsZSIsInJlZmVycmVyX2N1cnJlbnQiOiIiLCJyZWZlcnJpbmdfZG9tYWluX2N1cnJlbnQiOiIiLCJyZWxlYXNlX2NoYW5uZWwiOiJzdGFibGUiLCJjbGllZW50X2J1aWxkX251bWJlciI6MzQyOTY4LCJjbGllZW50X2V2ZW50X3NvdXJjZSI6bnVsbH0K90")
    req.Header.Set("Cookie", "__Secure-recent_mfa=gretr")
    req.Header.Set("Connection", "keep-alive")
    req.SetBodyString(`{"code":""}`)

    resp := fasthttp.AcquireResponse()
    defer fasthttp.ReleaseResponse(resp)

    if err := client.Do(req, resp); err != nil {
        return fmt.Errorf("initial PATCH request failed: %w", err)
    }

    if resp.StatusCode() != 401 {
        return fmt.Errorf("unexpected status from initial PATCH: %d (expected 401)", resp.StatusCode())
    }

    body2 := resp.Body()
    ticket, _, _, err := jsonparser.Get(body2, "mfa", "ticket")
    if err != nil {
        return fmt.Errorf("failed to parse MFA ticket: %w", err)
    }

    url = finishAPI
    req.Reset()
    req.SetRequestURI(url)
    req.Header.SetMethod("POST")
    req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPfU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/605.1")
    req.Header.Set("Accept", "*/*")
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Authorization", claimToken)
    req.Header.Set("X-Super-Properties", "eyJvcyI6ImlPUyIsImJyb3dzZXIiOiJTYWZhcmkiLCJkZXZpY2UiOiJpUGhvbmUgMTMiLCJzeXN0ZW1fbG9jYWxlIjoiZW4tVVMiLCJicm93c2VyX3VzZXJfYWdlbnQiOiJNb3ppbGxhLzUuMCAoaVBob25lOyBDUFUgaVBob25lIE9TIDE3XzAgbGlrZSBNYWMgT1MgWCkgQXBwbGVXZWJLaXQvNjA1LjEuMTUgKEtIVE1MLCBsaWtlIEdlY2tvKSBWZXJzaW9uLzE3LjAgTW9iaWxlLzE1RTE0OCBTYWZhcmkvNjA1LjEiLCJicm93c2VyX3ZlcnNpb24iOiIxNy4wIiwib3NfdmVyc2lvbiI6IjE3LjAiLCJyZWZlcnJlciI6Imh0dHBzOi8vd3d3Lmdvb2dsZS5jb20vIiwicmVmZXJyaW5nX2RvbWFpbiI6Ind3dy5nb29nbGUuY29tIiwic2VhcmNoX2VuZ2luZSI6Ikdvb2dsZSIsInJlZmVycmVyX2N1cnJlbnQiOiIiLCJyZWZlcnJpbmdfZG9tYWluX2N1cnJlbnQiOiIiLCJyZWxlYXNlX2NoYW5uZWwiOiJzdGFibGUiLCJjbGllZW50X2J1aWxkX251bWJlciI6MzQyOTY4LCJjbGllZW50X2V2ZW50X3NvdXJjZSI6bnVsbH0K")
    req.Header.Set("Cookie", "__dcfduid=e7b6ee30a8e411ef8ecc41593cf08537; __sdcfduid=e7b6ee31a8e411ef8ecc41593cf08537c253753bedf90229c3616ea0578975777e19c8a0dc10b674c5bdaff54a5379af; __cfruid=a0ba49dcc2b79c0e3e376c5e09feafc6fddce272-1732288656; _cfuvid=nD8IQG2ORW7s0.hSdzKwhYqA.SDOGGBe79ug4Jww4UE-1732288656540-0.0.1.1-604800000; cf_clearance=KW1p2GoSIMB8QAT2JgXtfPMRGghkf9LjI6V5G9Lzev4-1732288658-1.2.1.1-jDrOvliSA1ufVnZ3qwWq4R17N_YWMFmJAgLxk6unaZPTtoSiuTkyf7PP97mzIyVF31KE.RHawesWC.Gef2krf0IguMxvGqWtTcSTZ3hU.oLuRhNMM16zJ7AOWzk87T1zRUMGvZEIPcd396acEJxVM6rpCJSGZUAR5KtWiHccQ50nePAbw0ZafK0j4pDLAsuTp6v3II8jcuiXN9o4.84pKNSU0BKADKbWu1UslI86NNpUiqViYopKztYla9dpdeDZv372agVFd.PloOVDLngLCfNVjVxb_8INEsZNBxmYM8C6LKW5G_.Wi0l8H3497vCCReAq0_r.axUeuhGn8Q9Hqw")

payload := map[string]string{
    "ticket": string(ticket),
    "mfa_type": "password",
    "data": mfaPassword,
}
payloadBytes, _ := sonic.Marshal(payload) 
req.SetBody(payloadBytes)



    if err := client.Do(req, resp); err != nil {
        return fmt.Errorf("MFA finish POST failed: %w", err)
    }
    if resp.StatusCode() != 200 {
        return fmt.Errorf("unexpected status from MFA finish: %d", resp.StatusCode())
    }

    token, _, _, err := jsonparser.Get(resp.Body(), "token")
    if err != nil {
        return fmt.Errorf("failed to parse token from MFA finish response: %w", err)
    }

    s.mfaTOKEN = string(token)
    return nil
}


func claim_vanity(VanityURLCode string, mfaTOKEN string, detectionTime int64, originGuildID string, claimType string) {
	idx := currentGuildIndex.Load()
	if idx >= int32(len(claimGuildIDs)) {
		return
	}

	targetGuildID := claimGuildIDs[idx]
	targetURL := "https://canary.discord.com/api/v9/guilds/" + targetGuildID + "/vanity-url"
	payloadBody := []byte(`{"code":"` + VanityURLCode + `"}`) 

	var (
		wg                  sync.WaitGroup
		claimedSuccessfully int32
		numParallelClaims   int = 1
	)


	wg.Add(numParallelClaims)

	for i := 0; i < numParallelClaims; i++ {
		go func(attemptNum int) {
			defer wg.Done()

			if atomic.LoadInt32(&claimedSuccessfully) == 1 {
				return
			}

			req := fasthttp.AcquireRequest()
			resp := fasthttp.AcquireResponse()
			defer fasthttp.ReleaseRequest(req)
			defer fasthttp.ReleaseResponse(resp)

			staticHeaders.CopyTo(&req.Header)
			req.Header.Set("Authorization", claimToken)
			req.Header.SetMethodBytes([]byte("PATCH"))
			req.SetRequestURI(targetURL)
			req.SetBodyRaw(payloadBody)

			if mfaTOKEN != "" {
				req.Header.SetBytesV("X-Discord-MFA-Authorization", []byte(mfaTOKEN))
				req.Header.SetBytesV("Cookie", []byte("__Secure-recent_mfa="+mfaTOKEN))
			}

			start := time.Now()
			err := client.Do(req, resp)
			elapsed := time.Since(start)

			if atomic.LoadInt32(&claimedSuccessfully) == 1 && resp.StatusCode() != 200 {
				return
			}

			if err != nil {
				if atomic.LoadInt32(&claimedSuccessfully) == 0 {
					log.Printf("[Attempt %d] Error claiming vanity %s: %v. Took %s", attemptNum, VanityURLCode, err, elapsed)
				}
				return
			}

			statusCode := resp.StatusCode()
			responseBodyBytes := resp.Body() 

			if statusCode == 200 {
				if atomic.CompareAndSwapInt32(&claimedSuccessfully, 0, 1) {
					fmt.Printf("Claimed Vanity URL: %s in %dms (Attempt %d)\n", VanityURLCode, elapsed.Milliseconds(), attemptNum)

					sniper.guildMutex.Lock()
					for _, guild := range sniper.guilds {
						if guild.ID == targetGuildID {
							guild.ClaimedVanityURL = VanityURLCode
							break
						}
					}
					sniper.guildMutex.Unlock()

					if leaveAfterClaim && originGuildID != "" {
						go leaveGuild(originGuildID, monitorToken)
					}


if HOOK != "" {
	embed := EmbedClaimedWithType(VanityURLCode, elapsed.Milliseconds(), detectionTime, claimType)
	Notify(HOOK, embed)
}

					claimGuildIDs = append(claimGuildIDs[:idx], claimGuildIDs[idx+1:]...)
					go saveClaimGuilds()

					if idx < int32(len(claimGuildIDs)-1) {
						currentGuildIndex.Store(0)

					} else {
						currentGuildIndex.Store(0)
					}
				}
			} else {
				if atomic.LoadInt32(&claimedSuccessfully) == 0 {
					fmt.Printf("[Attempt %d] Failed to claim vanity: %s, status: %d, body: %s. Took %dms\n",
						attemptNum, VanityURLCode, statusCode, string(responseBodyBytes), elapsed.Milliseconds())
if HOOK != "" {
    embed := EmbedFailed(VanityURLCode, elapsed.Milliseconds(), string(responseBodyBytes) )
    Notify(HOOK, embed)
}

if statusCode == 400 && detectionTime > 0 && attemptNum == numParallelClaims {
                        if HOOK != "" {
}
					}
				}
			}
		}(i + 1)
	}

	go func() {
		wg.Wait()
		if atomic.LoadInt32(&claimedSuccessfully) == 0 {
		}
	}()
}





func Notify(webhookURL string, embed map[string]interface{}) {
	if webhookURL == "" {
		return
	}

	var payload []byte
	var err error

	if _, hasEmbeds := embed["embeds"]; hasEmbeds {
		payload, err = sonic.Marshal(embed)
	} else {
		payload, err = sonic.Marshal(map[string]interface{}{
			"embeds": []interface{}{embed},
		})
	}
	if err != nil {
		log.Println("Error encoding webhook payload:", err)
		return
	}

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)

	req.SetRequestURI(webhookURL)
	req.Header.SetMethod("POST")
	req.Header.Set("Content-Type", "application/json")
	req.SetBody(payload)

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	if err := client.Do(req, resp); err != nil {
		log.Println("Error sending webhook:", err)
		return
	}

	if resp.StatusCode() != 200 && resp.StatusCode() != 204 {
		log.Printf("Webhook returned status code: %d, body: %s\n", resp.StatusCode(), resp.Body())
	}
}




func loadConfig() error {
    data, err := os.ReadFile("config.json")
    if err != nil {
        return fmt.Errorf("failed to read config.json: %v", err)
    }

    monitorToken, err = jsonparser.GetString(data, "monitorToken")
    if err != nil {
        return fmt.Errorf("failed to parse monitorToken: %v", err)
    }

    claimToken, err = jsonparser.GetString(data, "claimToken")
    if err != nil {
        return fmt.Errorf("failed to parse claimToken: %v", err)
    }

    HOOK, err = jsonparser.GetString(data, "hook")
    if err != nil {
        HOOK = ""
    }

    mfaPassword, err = jsonparser.GetString(data, "mfa_password")
    if err != nil || mfaPassword == "" {
        return fmt.Errorf("failed to parse mfa_password from config.json: %v", err)
    }


    pc, err := jsonparser.GetBoolean(data, "parallelclaim")
    if err == nil {
        parallelClaim = pc
    }

    lac, err := jsonparser.GetBoolean(data, "leave_after_claim")
    if err == nil {
        leaveAfterClaim = lac
    }

    return nil
}

func loadClaimGuilds() error {
    file, err := os.Open("guilds.txt")
    if err != nil {
        if os.IsNotExist(err) {
            claimGuildIDs = []string{}
            return nil
        }
        return fmt.Errorf("failed to open guilds.txt: %v", err)
    }
    defer file.Close()

    scanner := bufio.NewScanner(file)
    claimGuildIDs = []string{}
    for scanner.Scan() {
        line := strings.TrimSpace(scanner.Text())
        if line != "" {
            claimGuildIDs = append(claimGuildIDs, line)
        }
    }

    if err := scanner.Err(); err != nil {
        return fmt.Errorf("error reading guilds.txt: %v", err)
    }

    return nil
}

func saveClaimGuilds() error {
    file, err := os.Create("guilds.txt")
    if err != nil {
        return fmt.Errorf("failed to create guilds.txt: %v", err)
    }
    defer file.Close()

    writer := bufio.NewWriter(file)
    for _, id := range claimGuildIDs {
        if _, err := writer.WriteString(id + "\n"); err != nil {
            return fmt.Errorf("failed to write to guilds.txt: %v", err)
        }
    }

    if err := writer.Flush(); err != nil {
        return fmt.Errorf("failed to flush guilds.txt: %v", err)
    }

    return nil
}


func EmbedClaimedWithType(vanity string, ms int64, detectionTime int64, claimType string) map[string]interface{} {
	var color int
	var context string
	switch claimType {
	case "GUILD_UPDATE":
		color = 0x00ff00 
		context = "GUILD_UPDATE"
	case "GUILD_DELETE":
		color = 0x000000 
		context = "GUILD_DELETE"
	default:
		color = 0x65f700 
		context = "UNKOWN"
	}

	return map[string]interface{}{
		"content": "@everyone",
		"embeds": []map[string]interface{}{
			{
				"title":       "Vanity Claimed",
				"description": fmt.Sprintf("**claim successful** - `%s`", vanity),
				"color":       color,
				"fields": []map[string]interface{}{
					{
						"name":   "Vanity",
						"value":  fmt.Sprintf("`%s`", vanity),
						"inline": true,
					},
					{
						"name":   "Claim Speed",
						"value":  fmt.Sprintf("`%d ms`", ms),
						"inline": true,
					},
					{
						"name":   "SHstamp",
						"value":  fmt.Sprintf("`%d`", detectionTime),
						"inline": true,
					},
					{
						"name":   "Context",
						"value":  context,
						"inline": true,
					},
				},
				"footer": map[string]interface{}{
					"text": "github.com/00nx",
				},
				"timestamp": time.Now().Format(time.RFC3339),
			},
		},
	}
}


func EmbedFailed(vanity string, ms int64, body string) map[string]interface{} {
	return map[string]interface{}{
		"title":       " Vanity Claim Failed",
		"description": fmt.Sprintf("**Claim  Failed ** - `%s`", vanity),
		"color":       0xff0033,
		"thumbnail": map[string]interface{}{
		},
		"image": map[string]interface{}{
		},
		"fields": []map[string]interface{}{
			{
				"name":   "Vanity URL",
				"value":  fmt.Sprintf("`%s`", vanity),
				"inline": true,
			},
			{
				"name":   "speed",
				"value":  fmt.Sprintf("`%d ms`", ms),
				"inline": true,
			},
			{
				"name":   " error",
				"value":  fmt.Sprintf("```%s```", body),
				"inline": false,
			},
		},
		"footer": map[string]interface{}{
			"text": "github.com/00nx",
		},
		"timestamp": time.Now().Format(time.RFC3339),
	}
}


func EmbedGuildDelete(guildID, guildName, vanityURL string) map[string]interface{} {
	return map[string]interface{}{
		"title":       "Guild Deleted",
		"description": "**sum guild got deleted **",
		"color":       0xff0033,
		"thumbnail": map[string]interface{}{
		},
		"fields": []map[string]interface{}{
			{
				"name":   " Guild Name",
				"value":  fmt.Sprintf("`%s`", guildName),
				"inline": true,
			},
			{
				"name":   " Guild ID",
				"value":  fmt.Sprintf("`%s`", guildID),
				"inline": true,
			},
			{
				"name":   " Vanity URL",
				"value":  fmt.Sprintf("`%s`", vanityURL),
				"inline": false,
			},
		},
		"footer": map[string]interface{}{
			"text": "github.com/00nx | System Log",
		},
		"timestamp": time.Now().Format(time.RFC3339),
	}
}








var sniper *Sniper

type NotifyEvent struct {
	WebhookURL string
	Embed      map[string]interface{}
}

var notifyChan = make(chan NotifyEvent, 1024)

func leaveGuild(guildID, monitorToken string) {
if guildID == "" || monitorToken == "" {
    if guildID == "" && monitorToken == "" {
    } else if guildID == "" {
    } else {
    }
    return
}


    req := fasthttp.AcquireRequest()
    resp := fasthttp.AcquireResponse()
    defer fasthttp.ReleaseRequest(req)
    defer fasthttp.ReleaseResponse(resp)

    req.SetRequestURI("https://discord.com/api/v9/users/@me/guilds/" + guildID)
    req.Header.SetMethod("DELETE")

    req.Header.Set("Accept", "*/*")
    req.Header.Set("Accept-Language", "en-US,en;q=0.9")
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Authorization", monitorToken)
    req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

    req.SetBody([]byte(`{"lurking":false}`))

    start := time.Now()
    err := client.Do(req, resp)
    elapsed := time.Since(start)

    if err != nil {
        log.Printf("Error leaving guild %s: %v (took %s)", guildID, err, elapsed)
        return
    }

    status := resp.StatusCode()
    if status == 204 || status == 200 {
    } else {
    }
}


func autoReloadGuilds() {
    now := time.Now().Unix()
    if now-lastGuildFileCheck < 3 { 
        return
    }
    lastGuildFileCheck = now

    info, err := os.Stat("guilds.txt")
    if err != nil {
        return
    }

    if info.ModTime().After(lastGuildFileModTime) {
        lastGuildFileModTime = info.ModTime()

        if err := loadClaimGuilds(); err != nil {
            log.Printf("[guilds.txt] reload failed: %v", err)
        } else {
            log.Printf(yellow+"[guilds.txt] reloaded (%d guild IDs)", len(claimGuildIDs))
        }
    }
}



func SetTitle(title string) {
    exec.Command("cmd", "/C", "title "+title).Run()
}


const (
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	cyan   = "\033[36m"
	reset  = "\033[0m"
)



func watchFileAndRestart(path string) {
    watcher, err := fsnotify.NewWatcher()
    if err != nil {
        log.Printf("[watcher] init failed: %v", err)
        return
    }
    defer watcher.Close()

    if err := watcher.Add(path); err != nil {
        log.Printf("[watcher] failed to watch %s: %v", path, err)
        return
    }


    debounce := time.NewTimer(time.Hour)
    debounce.Stop() 
    for {
        select {
        case event, ok := <-watcher.Events:
            if !ok {
                return
            }

            if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
                debounce.Reset(1 * time.Second)
            }

        case <-debounce.C:
            restartSelf()
            return

        case err, ok := <-watcher.Errors:
            if !ok {
			  log.Printf("[watcher] error: %v", err)
                return
            }
        }
    }
}

func restartSelf() {
    exe, err := os.Executable()
    if err != nil {
        return
    }

    cmd := exec.Command(exe, os.Args[1:]...)
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    cmd.Stdin = os.Stdin

    if err := cmd.Start(); err != nil {
        return
    }

    os.Exit(0)
}

func main() {
    if err := loadConfig(); err != nil {
        log.Fatalf("Config load failed: %v", err)
    }
    if err := loadClaimGuilds(); err != nil {
        log.Fatalf("Claim guilds load failed: %v", err)
    }


    go watchFileAndRestart("config.json")
    go watchFileAndRestart("guilds.txt")

    sniper = NewSniper()

    for {
        if err := genMFA(sniper); err != nil {
            log.Printf("[main] genMFA error: %v", err)
        }

        if sniper.mfaTOKEN != "" {
            break
        }

        log.Printf("[main] genMFA returned empty token, maybe token raped,  recreating sniper and retrying in 2s")
        time.Sleep(2 * time.Second)
        sniper = NewSniper()
    }

    fmt.Println(yellow + "Bypassed MFA, waiting 3 mins..." + reset)

    ticker := time.NewTicker(3 * time.Minute)
    defer ticker.Stop()
    defer sniper.Close()

    go func() {
        for {
            if err := sniper.Run(); err != nil {
                log.Printf("Tracker error: %v. Reconnecting beep boop niggers...", err)
                time.Sleep(2 * time.Second)
            }
        }
    }()

    go func() {
        for range ticker.C {
            if err := genMFA(sniper); err != nil {
                log.Printf("[main][periodic] genMFA error: %v", err)
            }
            if sniper.mfaTOKEN == "" {
                log.Printf("[main] background genMFA left token empty; retrying again gg")
                for i := 0; i < 3 && sniper.mfaTOKEN == ""; i++ {
                    time.Sleep(1 * time.Second)
                    if err := genMFA(sniper); err != nil {
                        log.Printf("[main][periodic][retry] genMFA error gg: %v", err)
                    }
                }
            }
        }
    }()

    go func() {
        for e := range notifyChan {
            Notify(e.WebhookURL, e.Embed)
        }
    }()



    stop := make(chan os.Signal, 1)
    signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
    <-stop

    fmt.Println("Sessions closed. exiting ts")
    sniper.Close()
}



