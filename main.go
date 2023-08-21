package main

import (
	"context"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gliderlabs/ssh"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
)

var SocksProxyAddr string

type localForwardChannelData struct {
	DestAddr string
	DestPort uint32

	OriginAddr string
	OriginPort uint32
}

func listKeys(dirPath string) (result []string, err error) {
	err = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, "_key") {
			result = append(result, path)
		}
		return nil
	})
	return
}

func directTCPIPClosure(rdb *redis.Client) ssh.ChannelHandler {
	return func(srv *ssh.Server, conn *gossh.ServerConn, newChan gossh.NewChannel, ctx ssh.Context) {
		d := localForwardChannelData{}
		if err := gossh.Unmarshal(newChan.ExtraData(), &d); err != nil {
			newChan.Reject(gossh.ConnectionFailed, "error parsing forward data: "+err.Error())
			return
		}

		ipAddr, err := net.ResolveIPAddr("ip4", d.DestAddr)
		if err != nil {
			ipAddr, err = net.ResolveIPAddr("ip6", d.DestAddr)
			if err != nil {
				newChan.Reject(gossh.Prohibited, "cannot resolve the said address: "+d.DestAddr)
				return
			}
		}

		dest := ipAddr.String()

		if srv.LocalPortForwardingCallback == nil || !srv.LocalPortForwardingCallback(ctx, dest, d.DestPort) {
			newChan.Reject(gossh.Prohibited, "illegal address")
			return
		}

		dest = net.JoinHostPort(dest, strconv.FormatInt(int64(d.DestPort), 10))

		var dialer net.Dialer
		var dconn net.Conn

		if len(SocksProxyAddr) != 0 {
			pDialer, err := proxy.SOCKS5("tcp", SocksProxyAddr, nil, proxy.Direct)
			if err != nil {
				newChan.Reject(gossh.ConnectionFailed, err.Error())
				return
			}
			dconn, err = pDialer.Dial("tcp", dest)
			if err != nil {
				newChan.Reject(gossh.ConnectionFailed, err.Error())
				return
			}
		} else {
			dconn, err = dialer.DialContext(ctx, "tcp", dest)
			if err != nil {
				newChan.Reject(gossh.ConnectionFailed, err.Error())
				return
			}
		}

		ch, reqs, err := newChan.Accept()
		if err != nil {
			dconn.Close()
			return
		}
		go gossh.DiscardRequests(reqs)

		go func() {
			defer ch.Close()
			defer dconn.Close()
			result, _ := io.Copy(ch, dconn)
			userID := ctx.User()
			rdb.HIncrBy(context.Background(), "ssh-server:users-usage", userID, result)
		}()
		go func() {
			defer ch.Close()
			defer dconn.Close()
			result, _ := io.Copy(dconn, ch)
			userID := ctx.User()
			rdb.HIncrBy(context.Background(), "ssh-server:users-usage", userID, result)
		}()
	}
}

func parseHostKeyFile(keyFile string) (ssh.Signer, error) {
	file, err := os.Open(keyFile)
	if err != nil {
		return nil, err
	}

	keyBytes, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	key, err := gossh.ParsePrivateKey(keyBytes)
	if err != nil {
		return nil, err
	}

	return key, nil
}

func main() {
	var err error
	if len(os.Args) == 2 {
		err = godotenv.Load(os.Args[1])
	} else {
		err = godotenv.Load()
	}
	if err != nil {
		log.Fatalln(err)
	}

	redisUrl, ok := os.LookupEnv("REDIS_URL")
	if !ok {
		log.Fatalln("REDIS_URL not provided. Consider adding it to .env or the environment variables")
	}

	listenAddr := os.Getenv("LISTEN_ADDR")
	if len(listenAddr) == 0 {
		listenAddr = ":2222"
	}

	SocksProxyAddr = os.Getenv("SOCKS_PROXY")

	hostKeyPath := os.Getenv("HOST_KEY_PATH")
	if len(hostKeyPath) == 0 {
		hostKeyPath = "/root/etc/ssh/"
	}

	maxConnString := os.Getenv("MAX_CONNECTIONS")
	maxConns, err := strconv.ParseInt(maxConnString, 10, 32)
	if maxConns == 0 || len(maxConnString) == 0 || err != nil {
		log.Fatalln("Invalid MAX_CONNECTIONS parameter")
	}

	defaultVersionString, ok := os.LookupEnv("DEFAULT_SERVER_VERSION")
	if !ok {
		log.Fatalln("DEFAULT_SERVER_VERSION not provided. Aborting")
	}
	if !strings.HasPrefix(defaultVersionString, "SSH-2.0-") {
		log.Fatalln("DEFAULT_SERVER_VERSION should start with `SSH-2.0-`")
	}
	defaultVersionString = defaultVersionString[8:]
	copyVersionString := os.Getenv("COPY_SERVER_VERSION")
	shouldCopyVersionString := true
	if len(copyVersionString) == 0 || strings.ToLower(copyVersionString) == "disabled" {
		shouldCopyVersionString = false
	}

	opts, err := redis.ParseURL(redisUrl)
	if err != nil {
		log.Fatalln(err)
	}
	rdb := redis.NewClient(opts) // This is safe to use concurrently
	pingRes := rdb.Ping(context.Background())
	_, err = pingRes.Result()
	if err != nil {
		log.Fatalf("Could not reach the redis server. Aborting: %v", err)
	}
	rdb.Del(context.Background(), "ssh-server:connections")
	var userConnectionCountMutex sync.Mutex
	server := ssh.Server{
		LocalPortForwardingCallback: ssh.LocalPortForwardingCallback(func(ctx ssh.Context, dhost string, dport uint32) bool {
			ip := net.ParseIP(dhost)
			if ip == nil {
				return false
			}
			result := ip.IsLoopback() || ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast() || ip.IsPrivate()
			return !result
		}),
		Addr: listenAddr,
		ChannelHandlers: map[string]ssh.ChannelHandler{
			"direct-tcpip": directTCPIPClosure(rdb),
		},
		PublicKeyHandler: func(ctx ssh.Context, key ssh.PublicKey) bool {
			//log.Printf("User %s with key %s", ctx.User(), gossh.MarshalAuthorizedKey(key))
			if len(ctx.User()) != 36 { // it isn't a UUID
				return false
			}
			userId := ctx.User()
			userString := userId + "::" + string(gossh.MarshalAuthorizedKey(key))
			userString = strings.Trim(userString, "\n\t\r")
			result := rdb.SIsMember(ctx, "ssh-server:users", userString)
			res, err := result.Result()
			doneCh := ctx.Done()
			if err != nil || !res || doneCh == nil {
				return false
			}
			userConnectionCountMutex.Lock()
			defer userConnectionCountMutex.Unlock()
			hget_res := rdb.HGet(ctx, "ssh-server:connections", userId)
			// It doesn't matter if we get an error (the key does not exist),
			// if there is something more serious it will be handled in HIncrBy
			connCntStr, _ := hget_res.Result()
			connCnt, err2 := strconv.ParseInt(connCntStr, 10, 32)
			if err2 == nil && connCnt >= maxConns {
				log.Printf("Client %s trying to have more than %d connections\n", userString, maxConns)
				return false // No duplicate connections
			}
			hincr_res := rdb.HIncrBy(ctx, "ssh-server:connections", userId, 1)
			if hincr_res.Err() != nil {
				return false
			}
			go func() {
				<-doneCh
				rdb.HIncrBy(context.Background(), "ssh-server:connections", userId, -1)
			}()
			return true
		},
		IdleTimeout: time.Minute * 1,
		MaxTimeout:  time.Hour * 6,
		Version:     defaultVersionString,
	}

	var versionStringMutex sync.Mutex // Not really used now, but can be helpful in the future
	go func() {
		if !shouldCopyVersionString {
			log.Println("Not copying the version string from another server")
			return
		}
		buf := make([]byte, 256)
		for {
			delayAmount := time.Hour * 1
			delayAmount += time.Millisecond * time.Duration(rand.Float32()*3600*1000)
			conn, err := net.Dial("tcp", copyVersionString)
			if err != nil {
				log.Printf("Could not copy the version string from another server: %v\n", err)
				time.Sleep(delayAmount)
				continue
			}
			n, err := conn.Read(buf)
			if err != nil || n == len(buf) {
				log.Printf("Invalid response from the to-be-copied ssh server, len=%d: %v\n", n, err)
				time.Sleep(delayAmount)
				conn.Close()
				continue
			}
			conn.Close()
			// Note! We should remove trailing zeros!
			resBuf := make([]byte, 0)
			for _, c := range buf {
				if c == 0 {
					break
				}
				resBuf = append(resBuf, c)
			}
			result := string(resBuf)
			result = strings.Trim(result, "\n\t\r")
			if !strings.HasPrefix(result, "SSH-2.0-") {
				log.Printf("The result from to-be-copied ssh server is invalid, does not start with `SSH-2.0-`")
				time.Sleep(delayAmount)
				continue
			}
			result = result[8:]
			versionStringMutex.Lock()
			server.Version = result
			versionStringMutex.Unlock()
			time.Sleep(delayAmount)
		}
	}()

	hostKeyFiles, err := listKeys(hostKeyPath)
	if err != nil {
		log.Fatalf("Could not get the host keys: %v\n", err)
	}
	for _, keyFile := range hostKeyFiles {
		hostKey, err := parseHostKeyFile(keyFile)
		if err != nil {
			log.Fatalf("Failed to parse host key file %s: %v", keyFile, err)
		}

		server.AddHostKey(hostKey)
	}

	time.Sleep(time.Second * 1) // Wait for the version string to settle in

	log.Printf("starting ssh-liberty-bridge on %s...\n", listenAddr)
	log.Fatal(server.ListenAndServe())
}
