package main

import (
	"context"
	"io"
	"log"
	"net"
	"os"
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

func DirectTCPIPHandler(srv *ssh.Server, conn *gossh.ServerConn, newChan gossh.NewChannel, ctx ssh.Context) {
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
		log.Printf("User %s read %d bytes", conn.Conn.User(), result)
	}()
	go func() {
		defer ch.Close()
		defer dconn.Close()
		result, _ := io.Copy(dconn, ch)
		log.Printf("User %s wrote %d bytes", conn.Conn.User(), result)
	}()
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
	opts, err := redis.ParseURL(redisUrl)
	if err != nil {
		log.Fatalln(err)
	}
	rdb := redis.NewClient(opts)
	rdb.Del(context.Background(), "ssh-server:connections")
	server := ssh.Server{
		LocalPortForwardingCallback: ssh.LocalPortForwardingCallback(func(ctx ssh.Context, dhost string, dport uint32) bool {
			// CAUTION!
			// The user can access everything (even the local network) on the server!
			return true
		}),
		Addr: listenAddr,
		ChannelHandlers: map[string]ssh.ChannelHandler{
			"direct-tcpip": DirectTCPIPHandler,
		},
		PublicKeyHandler: func(ctx ssh.Context, key ssh.PublicKey) bool {
			//log.Printf("User %s with key %s", ctx.User(), gossh.MarshalAuthorizedKey(key))
			if len(ctx.User()) != 36 { // it isn't a UUID
				return false
			}
			userString := ctx.User() + "-" + string(gossh.MarshalAuthorizedKey(key))
			userString = strings.Trim(userString, "\n\t")
			result := rdb.SIsMember(ctx, "ssh-server:users", userString)
			res, err := result.Result()
			doneCh := ctx.Done()
			if err != nil || !res || doneCh == nil {
				return false
			}
			sget_res := rdb.SIsMember(ctx, "ssh-server:connections", userString)
			res, err = sget_res.Result()
			if err != nil || res {
				log.Printf("Client %s trying to have duplicate connection\n", userString)
				return false // No duplicate connections
			}
			sadd_res := rdb.SAdd(ctx, "ssh-server:connections", userString)
			if sadd_res.Err() != nil {
				return false
			}
			go func() {
				<-doneCh
				rdb.SRem(context.Background(), "ssh-server:connections", userString)
			}()
			return true
		},
		IdleTimeout: time.Minute * 1,
		MaxTimeout:  time.Hour * 6,
	}
	hostKeyFiles := []string{
		"./etc/ssh/ssh_host_ecdsa_key",
		"./etc/ssh/ssh_host_ed25519_key",
		"./etc/ssh/ssh_host_rsa_key"} // Replace with your key file paths
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
