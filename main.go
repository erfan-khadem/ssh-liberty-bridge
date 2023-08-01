package main

import (
	"context"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gliderlabs/ssh"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	gossh "golang.org/x/crypto/ssh"
)

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

		if srv.LocalPortForwardingCallback == nil || !srv.LocalPortForwardingCallback(ctx, d.DestAddr, d.DestPort) {
			newChan.Reject(gossh.Prohibited, "port forwarding is disabled")
			return
		}

		dest := net.JoinHostPort(d.DestAddr, strconv.FormatInt(int64(d.DestPort), 10))

		var dialer net.Dialer
		dconn, err := dialer.DialContext(ctx, "tcp", dest)
		if err != nil {
			newChan.Reject(gossh.ConnectionFailed, err.Error())
			return
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
	hostKeyPath := os.Getenv("HOST_KEY_PATH")
	if len(hostKeyPath) == 0 {
		hostKeyPath = "/root/etc/ssh/"
	}
	maxConnString := os.Getenv("MAX_CONNECTIONS")
	maxConns, err := strconv.ParseInt(maxConnString, 10, 32)
	if maxConns == 0 || len(maxConnString) == 0 || err != nil {
		log.Fatalln("Invalid MAX_CONNECTIONS parameter")
	}
	opts, err := redis.ParseURL(redisUrl)
	if err != nil {
		log.Fatalln(err)
	}
	rdb := redis.NewClient(opts) // This is safe to use concurrently
	rdb.Del(context.Background(), "ssh-server:connections")
	server := ssh.Server{
		LocalPortForwardingCallback: ssh.LocalPortForwardingCallback(func(ctx ssh.Context, dhost string, dport uint32) bool {
			// CAUTION!
			// The user can access everything (even the local network) on the server!
			return true
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
			userString = strings.Trim(userString, "\n\t")
			result := rdb.SIsMember(ctx, "ssh-server:users", userString)
			res, err := result.Result()
			doneCh := ctx.Done()
			if err != nil || !res || doneCh == nil {
				return false
			}
			hget_res := rdb.HGet(ctx, "ssh-server:connections", userId)
			connCntStr, err := hget_res.Result()
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
	}

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

	log.Printf("starting ssh server on %s...\n", listenAddr)
	log.Fatal(server.ListenAndServe())
}
