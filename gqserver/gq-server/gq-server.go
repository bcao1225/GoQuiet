package main

import (
	"encoding/binary"
	"errors"
	"github.com/cbeuw/GoQuiet/gqserver"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

type pipe interface {
	remoteToServer()
	serverToRemote()
	closePipe()
}

type ssPair struct {
	ss     net.Conn
	remote net.Conn
}

type webPair struct {
	webServer net.Conn
	remote    net.Conn
}

func readTillDrain(conn net.Conn) (ret []byte, err error) {
	t := time.Now()
	t = t.Add(3 * time.Second)
	conn.SetReadDeadline(t) // 3 seconds
	_, err = conn.Read(ret)
	msglen := gqserver.BtoInt(ret[3:5])
	for len(ret) < msglen {
		var tempbuf []byte
		conn.Read(tempbuf)
		ret = append(ret, tempbuf...)
	}
	conn.SetReadDeadline(time.Time{})
	return
}

func (pair *webPair) closePipe() {
	pair.webServer.Close()
	pair.remote.Close()
}

func (pair *ssPair) closePipe() {
	pair.ss.Close()
	pair.remote.Close()
}

func (pair *webPair) serverToRemote() {
	_, err := io.Copy(pair.remote, pair.webServer)
	if err != nil {
		pair.closePipe()
	}
}

func (pair *webPair) remoteToServer() {
	for {
		_, err := io.Copy(pair.webServer, pair.remote)
		if err != nil {
			pair.closePipe()
			return
		}
	}
}

func (pair *ssPair) remoteToServer() {
	for {
		data, err := readTillDrain(pair.remote)
		if err != nil {
			pair.closePipe()
			return
		}
		data = data[5:]
		_, err = pair.ss.Write(data)
		if err != nil {
			pair.closePipe()
			return
		}
	}
}

func (pair *ssPair) serverToRemote() {
	for {
		data := []byte{}
		pair.ss.Read(data)
		msglen := make([]byte, 2)
		binary.BigEndian.PutUint16(msglen, uint16(len(data)))
		record := append([]byte{0x17, 0x03, 0x03}, msglen...)
		data = append(record, data...)
		_, err := pair.remote.Write(data)
		if err != nil {
			pair.closePipe()
			return
		}
	}
}

func dispatchConnection(conn net.Conn, sta *gqserver.State) {
	goWeb := func(data []byte) {
		pair, err := makeWebPipe(conn, sta)
		if err != nil {
			log.Fatal(err)
		}
		pair.webServer.Write(data)
		go pair.remoteToServer()
		go pair.serverToRemote()
	}
	goSS := func() {
		pair, err := makeSSPipe(conn, sta)
		if err != nil {
			log.Fatal(err)
		}
		go pair.remoteToServer()
		go pair.serverToRemote()
	}
	data := []byte{}
	conn.Read(data)
	ch, err := gqserver.ParseClientHello(data)
	if err != nil {
		goWeb(data)
		return
	}
	isSS := gqserver.IsSS(ch, sta)
	if !isSS {
		goWeb(data)
		return
	}

	reply := gqserver.ComposeReply(ch)
	_, err = conn.Write(reply)
	if err != nil {
		return
	}

	_, err = conn.Read(reply)
	if err != nil {
		return
	}
	goSS()
}

func makeWebPipe(remote net.Conn, sta *gqserver.State) (*webPair, error) {
	conn, err := net.Dial("tcp", sta.WebServerAddr)
	if err != nil {
		return &webPair{}, errors.New("Connection to web server failed")
	}
	pair := &webPair{
		conn,
		remote,
	}
	return pair, nil
}

func makeSSPipe(remote net.Conn, sta *gqserver.State) (*ssPair, error) {
	conn, err := net.Dial("tcp", sta.SS_LOCAL_HOST+":"+sta.SS_LOCAL_PORT)
	if err != nil {
		return &ssPair{}, errors.New("Connection to SS server failed")
	}
	pair := &ssPair{
		conn,
		remote,
	}
	return pair, nil
}

func usedRandomCleaner(sta *gqserver.State) {
	var mutex = &sync.Mutex{}
	for {
		time.Sleep(30 * time.Minute)
		now := int(sta.Now().Unix())
		mutex.Lock()
		for key, t := range sta.UsedRandom {
			if now-t > 1800 {
				delete(sta.UsedRandom, key)
			}
		}
		mutex.Unlock()
	}
}

func main() {
	sta := &gqserver.State{
		SS_LOCAL_HOST: os.Getenv("SS_LOCAL_HOST"),
		// Should be 127.0.0.1 unless the plugin is deployed on another machine, which is not supported yet
		SS_LOCAL_PORT: os.Getenv("SS_LOCAL_PORT"),
		// SS loopback port, default set by SS to 8388
		SS_REMOTE_HOST: os.Getenv("SS_REMOTE_HOST"),
		// Outbound listening address, should be 0.0.0.0
		SS_REMOTE_PORT: os.Getenv("SS_REMOTE_PORT"),
		// Since this is a TLS obfuscator, this should be 443
		Now:        time.Now,
		UsedRandom: map[[32]byte]int{},
	}
	err := gqserver.ParseConfig(os.Args[1], sta)
	if err != nil {
		panic(err)
	}
	gqserver.MakeAESKey(sta)
	go usedRandomCleaner(sta)
	listener, _ := net.Listen("tcp", sta.SS_REMOTE_HOST+":"+sta.SS_REMOTE_PORT)
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("%v", err)
			continue
		}
		go dispatchConnection(conn, sta)
	}

}
