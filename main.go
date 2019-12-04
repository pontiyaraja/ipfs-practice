package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"mime/multipart"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	example "github.com/KIPFoundation/wyoming-test/example"
	"github.com/go-redis/redis"
	"github.com/golang/protobuf/proto"

	crand "crypto/rand"

	"github.com/KIPFoundation/kfs-poc/filecore"
	kiplog "github.com/KIPFoundation/kip-log"
	ds "github.com/ipfs/go-datastore"
	"github.com/ipfs/go-datastore/autobatch"
	"github.com/ipfs/go-datastore/query"
	crdt "github.com/ipfs/go-ds-crdt"
	shell "github.com/ipfs/go-ipfs-api"
	files "github.com/ipfs/go-ipfs-files"
	u "github.com/ipfs/go-ipfs-util"
	logging "github.com/ipfs/go-log"
	"github.com/libp2p/go-libp2p"
	crypto "github.com/libp2p/go-libp2p-core/crypto"
	peer "github.com/libp2p/go-libp2p-core/peer"
	dht "github.com/libp2p/go-libp2p-kad-dht"

	corecrypto "github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/host"
	pubsub "github.com/libp2p/go-libp2p-pubsub"

	ipfslite "github.com/hsanjuan/ipfs-lite"
	"github.com/mitchellh/go-homedir"

	cid "github.com/ipfs/go-cid"
	multiaddr "github.com/multiformats/go-multiaddr"
)

var (
	logger = logging.Logger("globaldb")
	//listen, _ = multiaddr.NewMultiaddr("/ip4/0.0.0.0/tcp/33123")
	listen, _ = multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/9096")
	topic     = "globaldb-example"
	netTopic  = "globaldb-example-net"
	config    = "globaldb-example"
)

func mains1() {
	// Bootstrappers are using 1024 keys. See:
	// https://github.com/ipfs/infra/issues/378
	corecrypto.MinRsaKeyBits = 1024

	logging.SetLogLevel("*", "error")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir, err := homedir.Dir()
	if err != nil {
		logger.Fatal(err)
	}
	data := filepath.Join(dir, "globaldb-example")

	store, err := ipfslite.BadgerDatastore(data)
	if err != nil {
		logger.Fatal(err)
	}
	defer store.Close()

	keyPath := filepath.Join(data, "key")
	var priv crypto.PrivKey
	_, err = os.Stat(keyPath)
	if os.IsNotExist(err) {
		priv, _, err = crypto.GenerateKeyPair(crypto.Ed25519, 1)
		if err != nil {
			logger.Fatal(err)
		}
		data, err := crypto.MarshalPrivateKey(priv)
		if err != nil {
			logger.Fatal(err)
		}
		err = ioutil.WriteFile(keyPath, data, 0400)
		if err != nil {
			logger.Fatal(err)
		}
	} else if err != nil {
		logger.Fatal(err)
	} else {
		key, err := ioutil.ReadFile(keyPath)
		if err != nil {
			logger.Fatal(err)
		}
		priv, err = crypto.UnmarshalPrivateKey(key)
		if err != nil {
			logger.Fatal(err)
		}

	}
	pid, err := peer.IDFromPublicKey(priv.GetPublic())
	if err != nil {
		logger.Fatal(err)
	}

	h, dht, err := ipfslite.SetupLibp2p(
		ctx,
		priv,
		nil,
		[]multiaddr.Multiaddr{listen},
		libp2p.ChainOptions(),
	)
	if err != nil {
		logger.Fatal(err)
	}
	defer h.Close()
	defer dht.Close()

	psub, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		logger.Fatal(err)
	}

	netSubs, err := psub.Subscribe(netTopic)
	if err != nil {
		logger.Fatal(err)
	}

	// Use a special pubsub topic to avoid disconnecting
	// from globaldb peers.
	go func() {
		for {
			msg, err := netSubs.Next(ctx)
			if err != nil {
				fmt.Println(err)
				break
			}
			fmt.Println("Messge received ", msg.GetFrom().Pretty(), string(msg.GetData()))
			h.ConnManager().TagPeer(msg.GetFrom(), "keep", 100)
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				psub.Publish(netTopic, []byte("hi!"))
				time.Sleep(20 * time.Second)
			}
		}
	}()

	ipfs, err := ipfslite.New(ctx, store, h, dht, nil)
	if err != nil {
		logger.Fatal(err)
	}

	pubsubBC, err := crdt.NewPubSubBroadcaster(ctx, psub, "globaldb-example")
	if err != nil {
		logger.Fatal(err)
	}

	go func() {
		for {
			msg, err := pubsubBC.Next()
			if err != nil {
				fmt.Println(err)
				break
			}
			c, e := cid.Cast(msg)
			fmt.Println("Messge received ", c.String(), e)
			//h.ConnManager().TagPeer(msg.GetFrom(), "keep", 100)
		}
	}()

	opts := crdt.DefaultOptions()
	opts.Logger = logger
	opts.RebroadcastInterval = 5 * time.Second
	opts.PutHook = func(k ds.Key, v []byte) {
		fmt.Printf("Added: [%s] -> %s\n", k, string(v))

	}
	opts.DeleteHook = func(k ds.Key) {
		fmt.Printf("Removed: [%s]\n", k)
	}

	crdt, err := crdt.New(store, ds.NewKey("crdt"), ipfs, pubsubBC, opts)
	if err != nil {
		logger.Fatal(err)
	}
	defer crdt.Close()

	fmt.Println("Bootstrapping...")

	addrs := []string{
		"/ip4/104.236.179.241/tcp/4001/ipfs/QmSoLPppuBtQSGwKDZT2M73ULpjvfd3aZ6ha4oFGL1KrGM",
		"/ip4/128.199.219.111/tcp/4001/ipfs/QmSoLSafTMBsPKadTEgaXctDQVcqN88CNLHXMkTNwMKPnu",
		"/ip4/178.62.158.247/tcp/4001/ipfs/QmSoLer265NRgSp2LA3dPaeykiS1J6DifTC88f5uVQKNAd",
		"/ip4/139.59.25.7/tcp/4001/ipfs/QmQCyd1C3YRjWHt3Ri2MDM4yirFHPyMqA3nFS6MYDMCkP2",
		"/ip4/127.0.0.1/tcp/4001/ipfs/QmT6oyeAKySLfMqjEC1qGJfNubxD5VhJX8q57NfUfpztuc"}

	//bstr, _ := multiaddr.NewMultiaddr("/ip4/94.130.135.167/tcp/33123/ipfs/12D3KooWFta2AE7oiK1ioqjVAKajUJauZWfeM7R413K7ARtHRDAu")
	bstr, _ := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/4001/ipfs/QmT6oyeAKySLfMqjEC1qGJfNubxD5VhJX8q57NfUfpztuc")
	inf, _ := peer.AddrInfoFromP2pAddr(bstr)
	//list := append(ipfslite.DefaultBootstrapPeers(), *inf)
	//ipfs.Bootstrap(list)

	prAddrs, err := ParseBootstrapPeers(addrs)
	if err != nil {
		logger.Fatal(err)
	}
	list := append(prAddrs, *inf)
	ipfs.Bootstrap(list)

	//ipfs.Bootstrap([]peer.AddrInfo{*inf})
	h.ConnManager().TagPeer(inf.ID, "keep", 100)

	fmt.Printf(`
Peer ID: %s
Listen address: %s
Topic: %s
Data Folder: %s

Ready!

Commands:

> list               -> list items in the store
> get <key>          -> get value for a key
> put <key> <value>  -> store value on a key
> exit               -> quit


`,
		pid, listen, topic, data,
	)
	fmt.Printf("%s - %d connected peers\n", time.Now().Format(time.Stamp), len(connectedPeers(h)))
	peerIDs := connectedPeers(h)
	for _, pr := range peerIDs {
		fmt.Println(pr.ID, pr.Addrs)
	}
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		fmt.Println("Running in daemon mode")
		go func() {
			for {
				fmt.Printf("%s - %d connected peers\n", time.Now().Format(time.Stamp), len(connectedPeers(h)))
				peerIDs := connectedPeers(h)
				for _, pr := range peerIDs {
					fmt.Println(pr.ID, pr.Addrs)
				}
				time.Sleep(10 * time.Second)
			}
		}()
		signalChan := make(chan os.Signal, 20)
		signal.Notify(
			signalChan,
			syscall.SIGINT,
			syscall.SIGTERM,
			syscall.SIGHUP,
		)
		<-signalChan
		return
	}

	fmt.Printf("> ")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		text := scanner.Text()
		fields := strings.Fields(text)
		if len(fields) == 0 {
			fmt.Printf("> ")
			continue
		}

		cmd := fields[0]

		switch cmd {
		case "exit", "quit":
			return
		case "debug":
			if len(fields) < 2 {
				fmt.Println("debug <on/off/peers>")
			}
			st := fields[1]
			switch st {
			case "on":
				logging.SetLogLevel("globaldb", "debug")
			case "off":
				logging.SetLogLevel("globaldb", "error")
			case "peers":
				for _, p := range connectedPeers(h) {
					addrs, err := peer.AddrInfoToP2pAddrs(p)
					if err != nil {
						logger.Warning(err)
						continue
					}
					for _, a := range addrs {
						fmt.Println(a)
					}
				}
			}
		case "list":
			q := query.Query{}
			results, err := crdt.Query(q)
			if err != nil {
				printErr(err)
			}
			for r := range results.Next() {
				if r.Error != nil {
					printErr(err)
					continue
				}
				fmt.Printf("[%s] -> %s\n", r.Key, string(r.Value))
			}
		case "get":
			if len(fields) < 2 {
				fmt.Println("get <key>")
				fmt.Println("> ")
				continue
			}
			k := ds.NewKey(fields[1])
			v, err := crdt.Get(k)
			if err != nil {
				printErr(err)
				continue
			}
			fmt.Printf("[%s] -> %s\n", k, string(v))
		case "put":
			if len(fields) < 3 {
				fmt.Println("put <key> <value>")
				fmt.Println("> ")
				continue
			}
			k := ds.NewKey(fields[1])
			v := strings.Join(fields[2:], " ")
			err := crdt.Put(k, []byte(v))
			if err != nil {
				printErr(err)
				continue
			}
		}
		fmt.Printf("> ")
	}
}

func printErr(err error) {
	fmt.Println("error:", err)
	fmt.Println("> ")
}

func connectedPeers(h host.Host) []*peer.AddrInfo {
	var pinfos []*peer.AddrInfo
	for _, c := range h.Network().Conns() {
		pinfos = append(pinfos, &peer.AddrInfo{
			ID:    c.RemotePeer(),
			Addrs: []multiaddr.Multiaddr{c.RemoteMultiaddr()},
		})
	}
	return pinfos
}

// ParseBootstrapPeer parses a bootstrap list into a list of AddrInfos.
func ParseBootstrapPeers(addrs []string) ([]peer.AddrInfo, error) {
	maddrs := make([]multiaddr.Multiaddr, len(addrs))
	for i, addr := range addrs {
		var err error
		maddrs[i], err = multiaddr.NewMultiaddr(addr)
		if err != nil {
			return nil, err
		}
	}
	return peer.AddrInfosFromP2pAddrs(maddrs...)
}

type User struct {
	UserName string `json:"username"`
	Password string `json:"password"`
}

var jsonBytes1 = []byte(`
{
	"key1":{
		"Item1": "Value1",
		"Item2": 1},
	"key2":{
		"Item1": "Value2",
		"Item2": 2},
	"key3":{
		"Item1": "Value3",
		"Item2": 3}
}`)

var jsonBytes = []byte(`{"key":{"_id":1}, "value":{"value":"test"}}`)

// Item struct; we want to create these from the JSON above
type Item struct {
	Item1 string
	Item2 int
}

// Implement the String interface for pretty printing Items
func (i Item) String() string {
	return fmt.Sprintf("Item: %s, %d", i.Item1, i.Item2)
}

var sh *shell.Shell
var ncalls int

var _ = time.ANSIC

func sleep() {
	ncalls++
	//time.Sleep(time.Millisecond * 5)
}

func randString() string {
	alpha := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890"
	l := rand.Intn(10) + 2

	var s string
	for i := 0; i < l; i++ {
		s += string([]byte{alpha[rand.Intn(len(alpha))]})
	}
	return s
}

func makeRandomObject() (string, error) {
	// do some math to make a size
	x := rand.Intn(120) + 1
	y := rand.Intn(120) + 1
	z := rand.Intn(120) + 1
	size := x * y * z

	r := io.LimitReader(u.NewTimeSeededRand(), int64(size))
	sleep()
	return sh.Add(r)
}

func makeRandomDir(depth int) (string, error) {
	if depth <= 0 {
		return makeRandomObject()
	}
	sleep()
	empty, err := sh.NewObject("unixfs-dir")
	if err != nil {
		return "", err
	}

	curdir := empty
	for i := 0; i < 10; i++ {
		fmt.Println("Empty data ----- >  ", empty)
		var obj string
		if rand.Intn(2) == 1 {
			obj, err = makeRandomObject()
			if err != nil {
				return "", err
			}
		} else {
			obj, err = makeRandomDir(depth - 1)
			if err != nil {
				return "", err
			}
		}

		name := randString()
		sleep()
		nobj, err := sh.PatchLink(curdir, name, obj, true)
		if err != nil {
			return "", err
		}
		curdir = nobj
	}

	return curdir, nil
}

func main9() {
	sh = shell.NewShell("localhost:5001")
	for i := 0; i < 20; i++ {
		_, err := makeRandomObject()
		if err != nil {
			fmt.Println("err: ", err)
			return
		}
	}
	//sh.Add("")
	fmt.Println("we're okay")

	out, err := makeRandomDir(10)
	fmt.Printf("%d calls\n", ncalls)
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println("Out  ===   ", out)
	// for {
	// 	time.Sleep(time.Second * 1000)
	// }
}

func main1() {

	// Unmarshal using a generic interface
	var f interface{}
	err := json.Unmarshal(jsonBytes, &f)
	if err != nil {
		fmt.Println("Error parsing JSON: ", err)
	}

	// JSON object parses into a map with string keys
	itemsMap := f.(map[string]interface{})

	// Loop through the Items; we're not interested in the key, just the values
	for k, v := range itemsMap {
		fmt.Println("Hereeeeeeeeeeeee", k, v)
		// Use type assertions to ensure that the value's a JSON object
		// switch jsonObj := v.(type) {
		// // The value is an Item, represented as a generic interface
		// case interface{}:
		// 	var item Item
		// 	// Access the values in the JSON object and place them in an Item
		// 	for itemKey, itemValue := range jsonObj.(map[string]interface{}) {
		// 		switch itemKey {
		// 		case "Item1":
		// 			// Make sure that Item1 is a string
		// 			switch itemValue := itemValue.(type) {
		// 			case string:
		// 				item.Item1 = itemValue
		// 			default:
		// 				fmt.Println("Incorrect type for", itemKey)
		// 			}
		// 		case "Item2":
		// 			// Make sure that Item2 is a number; all numbers are transformed to float64
		// 			switch itemValue := itemValue.(type) {
		// 			case float64:
		// 				item.Item2 = int(itemValue)
		// 			default:
		// 				fmt.Println("Incorrect type for", itemKey)
		// 			}
		// 		default:
		// 			fmt.Println("Unknown key for Item found in JSON")
		// 		}
		// 	}
		// 	fmt.Println(item)
		// // Not a JSON object; handle the error
		// default:
		// 	fmt.Println("Expecting a JSON object; got something else")
		// }
	}

}

var resp chan string

func main2() {
	resp = make(chan string, 2)
	messages := make(chan string, 2)

	// Because this channel is buffered, we can send these
	// values into the channel without a corresponding
	// concurrent receive.
	//messages <- ""
	//messages <- ""
	inp := []string{"buffered", "channel"}
	for _, data := range inp {
		messages <- data
		fmt.Println(data)
		go appendD(data)
	}

	// Later we can receive these two values as usual.
	fmt.Println(<-messages)
	fmt.Println(<-resp)
	fmt.Println(<-messages)
	fmt.Println(<-resp)
}

func appendD(messages string) {
	msg := messages
	fmt.Println("append", msg)
	resp <- "hi" + msg
}

func main_2() {
	// priv, pub, err := crypto.GenerateKeyPair(atyp, *size)
	// if err != nil {
	// 	fmt.Fprintln(os.Stderr, err)
	// 	os.Exit(1)
	// }
	//randData := strings.NewReader("moibit")
	priKey, err := rsa.GenerateKey(crand.Reader, 1024)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ciphertext, err := rsa.EncryptOAEP(sha256.New(), crand.Reader, &priKey.PublicKey, []byte("bafybeigu2vrvlnzoebbm4dcn6kqhkcx63awtslqguc2rd4nafk3uedmu2a"), []byte("auth"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error from encryption: %s\n", err)
		return
	}

	// Since encryption is a randomized function, ciphertext will be
	// different each time.
	fmt.Printf("Ciphertext: %x\n", ciphertext)

	//rsa.SignPSS(crand.Reader, priKey)

	// data, err := base64.StdEncoding.EncodeToString(ciphertext)
	// if err != nil {
	// 	panic(err)
	// }
	//base64.StdEncoding.EncodeToString(data)
	// data := base64.StdEncoding.EncodeToString(ciphertext)
	// fmt.Println(data)

	// data1, err := base64.StdEncoding.DecodeString(data)
	// if err != nil {
	// 	panic(err)
	// }
	// fmt.Println(string(data1))
	fmt.Println("Hex : ", hex.EncodeToString(ciphertext))

	bt, err := hex.DecodeString(hex.EncodeToString(ciphertext))
	if err != nil {
		panic(err)
	}

	byteDat, err := rsa.DecryptOAEP(sha256.New(), crand.Reader, priKey, bt, []byte("auth"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Println("Decrypted Data ", string(byteDat))

	bts := fmt.Sprintf("%x", priKey.D.Bytes())

	fmt.Println("Private key  -----  >  ", bts)

	pByt, err := x509.MarshalPKCS8PrivateKey(priKey)
	if err != nil {
		panic(err)
	}
	fmt.Println()
	pkStr := base64.StdEncoding.EncodeToString(pByt)
	fmt.Println("PKEY String        :     ", pkStr)

	pkByt, err := base64.StdEncoding.DecodeString(pkStr)
	if err != nil {
		panic(err)
	}

	pkey, err := x509.ParsePKCS8PrivateKey(pkByt)
	if err != nil {
		panic(err)
	}

	byteDat1, err := rsa.DecryptOAEP(sha256.New(), crand.Reader, pkey.(*rsa.PrivateKey), ciphertext, []byte("auth"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Println("Decrypted Data 1111", string(byteDat1))

	// ctx, cancel := context.WithCancel(context.Background())
	// defer cancel()

	// log.SetLogLevel("*", "warn")

	// ds, err := ipfslite.BadgerDatastore("test")
	// if err != nil {
	// 	panic(err)
	// }
	// priv, _, err := crypto.GenerateKeyPair(crypto.RSA, 2048)
	// if err != nil {
	// 	panic(err)
	// }

	// listen, _ := multiaddr.NewMultiaddr("/ip4/0.0.0.0/tcp/4005")

	// h, dht, err := ipfslite.SetupLibp2p(
	// 	ctx,
	// 	priv,
	// 	nil,
	// 	[]multiaddr.Multiaddr{listen},
	// )

	// if err != nil {
	// 	panic(err)
	// }

	// lite, err := ipfslite.New(ctx, ds, h, dht, nil)
	// if err != nil {
	// 	panic(err)
	// }

	// lite.Bootstrap(ipfslite.DefaultBootstrapPeers())

	// //lite.Set()

	// c, _ := cid.Decode("QmRYkk4tsV6MuqVCojB4dfgfs1Zw7MW6mF7C3W6CXsz4jA")
	// rsc, err := lite.GetFile(ctx, c)
	// if err != nil {
	// 	panic(err)
	// }
	// defer rsc.Close()
	// content, err := ioutil.ReadAll(rsc)
	// if err != nil {
	// 	panic(err)
	// }

	// fmt.Println(string(content))
}

func main14() {
	dStore, err := NewDatastore("/Users/pandiyarajaramamoorthy/new/")
	if err != nil {
		fmt.Println("error ---------------------------   ", err)
	}
	key := ds.NewKey("root")
	dStore.Put(key, []byte(`{"test key":"test value"}`))
	val, err := dStore.Get(key)
	fmt.Println("Value ============    ", string(val))
	fmt.Println("Error ==============  ", err)

	dStore.Put(key, []byte(`{"test key1":"test value1"}`))
	val, err = dStore.Get(key)
	fmt.Println("Value ============    ", string(val))
	fmt.Println("Error ==============  ", err)

	child := ds.NewMapDatastore()
	d := autobatch.NewAutoBatching(child, 16)
	d.Put(key, []byte(`{"Batch key1":"Batch value1"}`))
	val, err = d.Get(key)
	fmt.Println("Value ============    ", string(val))
	fmt.Println("Error ==============  ", err)
	fmt.Println(d.Flush())
}

var ObjectKeySuffix = ".dsobject"

// Datastore uses a uses a file per key to store values.
type Datastore struct {
	path string
}

// NewDatastore returns a new fs Datastore at given `path`
func NewDatastore(path string) (ds.Datastore, error) {
	if !isDir(path) {
		return nil, fmt.Errorf("Failed to find directory at: %v (file? perms?)", path)
	}

	return &Datastore{path: path}, nil
}

// KeyFilename returns the filename associated with `key`
func (d *Datastore) KeyFilename(key ds.Key) string {
	return filepath.Join(d.path, key.String(), ObjectKeySuffix)
}

// Put stores the given value.
func (d *Datastore) Put(key ds.Key, value []byte) (err error) {
	fn := d.KeyFilename(key)

	// mkdirall above.
	err = os.MkdirAll(filepath.Dir(fn), 0755)
	if err != nil {
		return err
	}

	return ioutil.WriteFile(fn, value, 0666)
}

// Get returns the value for given key
func (d *Datastore) Get(key ds.Key) (value []byte, err error) {
	fn := d.KeyFilename(key)
	if !isFile(fn) {
		return nil, ds.ErrNotFound
	}

	return ioutil.ReadFile(fn)
}

// Has returns whether the datastore has a value for a given key
func (d *Datastore) Has(key ds.Key) (exists bool, err error) {
	return ds.GetBackedHas(d, key)
}

func (d *Datastore) GetSize(key ds.Key) (size int, err error) {
	return ds.GetBackedSize(d, key)
}

// Delete removes the value for given key
func (d *Datastore) Delete(key ds.Key) (err error) {
	fn := d.KeyFilename(key)
	if !isFile(fn) {
		return nil
	}

	err = os.Remove(fn)
	if os.IsNotExist(err) {
		err = nil // idempotent
	}
	return err
}

// Query implements Datastore.Query
func (d *Datastore) Query(q query.Query) (query.Results, error) {

	results := make(chan query.Result)

	walkFn := func(path string, info os.FileInfo, err error) error {
		// remove ds path prefix
		if strings.HasPrefix(path, d.path) {
			path = path[len(d.path):]
		}

		if !info.IsDir() {
			if strings.HasSuffix(path, ObjectKeySuffix) {
				path = path[:len(path)-len(ObjectKeySuffix)]
			}
			var result query.Result
			key := ds.NewKey(path)
			result.Entry.Key = key.String()
			if !q.KeysOnly {
				result.Entry.Value, result.Error = d.Get(key)
			}
			results <- result
		}
		return nil
	}

	go func() {
		filepath.Walk(d.path, walkFn)
		close(results)
	}()
	r := query.ResultsWithChan(q, results)
	r = query.NaiveQueryApply(q, r)
	return r, nil
}

// isDir returns whether given path is a directory
func isDir(path string) bool {
	finfo, err := os.Stat(path)
	if err != nil {
		return false
	}

	return finfo.IsDir()
}

// isFile returns whether given path is a file
func isFile(path string) bool {
	finfo, err := os.Stat(path)
	if err != nil {
		return false
	}

	return !finfo.IsDir()
}

func (d *Datastore) Close() error {
	return nil
}

func (d *Datastore) Batch() (ds.Batch, error) {
	return ds.NewBasicBatch(d), nil
}

// DiskUsage returns the disk size used by the datastore in bytes.
func (d *Datastore) DiskUsage() (uint64, error) {
	var du uint64
	err := filepath.Walk(d.path, func(p string, f os.FileInfo, err error) error {
		if err != nil {
			log.Println(err)
			return err
		}
		if f != nil {
			du += uint64(f.Size())
		}
		return nil
	})
	return du, err
}

func mainPan() {
	test := &example.Test{
		Label: proto.String("hello"),
		Type:  proto.Int32(17),
		Reps:  []int64{1, 2, 3},
	}
	data, err := proto.Marshal(test)
	if err != nil {
		log.Fatal("marshaling error: ", err)
	}
	fmt.Println("----------", string(data))
	newTest := &example.Test{}
	err = proto.Unmarshal(data, newTest)
	if err != nil {
		log.Fatal("unmarshaling error: ", err)
	}
	fmt.Println("Proto message string =====================   ", test.String())
	// Now test and newTest contain the same data.
	if test.GetLabel() != newTest.GetLabel() {
		log.Fatalf("data mismatch %q != %q", test.GetLabel(), newTest.GetLabel())
	}

	//resp, err := http.Get("http://localhost:5001/api/v0/dht/put?arg=<key>&arg=<value>&verbose=<value>")

	//emails := []string{"a1@gmail.com", "b1@gmail.com", "c1@gmail.com", "d1@gmail.com", "e1@gmail.com", "f1@gmail.com", "g1@gmail.com", "h1@gmail.com", "i1@gmail.com", "j1@gmail.com"}
	// fileUrls := []string{"/Users/pandiyarajaramamoorthy/Downloads/PDFs/Airtel Invoice.pdf",
	// 	"/Users/pandiyarajaramamoorthy/Downloads/PDFs/Documentation - The Go Programming Language.pdf",
	// 	"/Users/pandiyarajaramamoorthy/Downloads/PDFs/G III English Skill Enhancement Work Sheet 7 (23-08-2019).pdf",
	// 	"/Users/pandiyarajaramamoorthy/Downloads/PDFs/G-III Hindi Half Yearly MQP.pdf",
	// 	"/Users/pandiyarajaramamoorthy/Downloads/PDFs/G-III Kannada Half Yearly MQP.pdf",
	// 	"/Users/pandiyarajaramamoorthy/Downloads/PDFs/Grade III-English-Half Yearly MQP.pdf",
	// 	"/Users/pandiyarajaramamoorthy/Downloads/PDFs/Grade III-Maths-Half Yearly MQP.pdf",
	// 	"/Users/pandiyarajaramamoorthy/Downloads/PDFs/Grade III-Science- Half Yearly MQP.pdf",
	// 	"/Users/pandiyarajaramamoorthy/Downloads/PDFs/Grade III-Social-Half Yearly MQP.pdf",
	// 	"/Users/pandiyarajaramamoorthy/Downloads/PDFs/Learning-Go-latest.pdf",
	// 	"/Users/pandiyarajaramamoorthy/Downloads/PDFs/RamanKumar.pdf",
	// 	"/Users/pandiyarajaramamoorthy/Downloads/PDFs/SC-02.pdf",
	// 	"/Users/pandiyarajaramamoorthy/Downloads/PDFs/Shrikar-moibit receipt.pdf",
	// 	"/Users/pandiyarajaramamoorthy/Downloads/PDFs/data_structures_algorithms_tutorial.pdf",
	// 	"/Users/pandiyarajaramamoorthy/Downloads/PDFs/go.pdf"}
	don := make(chan bool)
	// for _, mail := range emails {
	// 	fmt.Println("Email Out  :  ", mail)
	// 	go execute(mail)
	// }

	// for i, fName := range fileUrls {
	// 	sendPostRequest("http://localhost:9023/moibit/writefile", fName, "text/plain", "/test3/testTxt"+strconv.Itoa(i)+".pdf")
	// }

	ctx := context.Background()
	h, err := libp2p.New(context.Background())
	if err != nil {
		fmt.Println("libp2p error    ", err)
		panic(err)
	}
	dStore, err := NewDatastore("/Users/pandiyarajaramamoorthy/new/")
	if err != nil {
		fmt.Println("error ---------------------------   ", err)
	}
	ipfsDHT := dht.NewDHT(ctx, h, dStore.(ds.Batching))
	ipfsDHT.RoutingTable()
	fmt.Println("Peeer ID      0000000000000000000000    ", ipfsDHT.PeerID())
	if err = ipfsDHT.PutValue(ctx, "/v1/myhash", []byte("Hello test")); err != nil {
		fmt.Println("put error    ", err)
		panic(err)
	}
	value, err := ipfsDHT.GetValue(ctx, "/v/myhash")
	if err != nil {
		fmt.Println("get error    ", err)
		panic(err)
	}
	fmt.Println("Got Value ==============  ", value)
	///v/hello
	<-don
}

func execute(mail string) {
	fmt.Println("Email  :  ", mail)
	resp, err := http.Get("http://localhost:9023/moibit/generateauth?email=" + mail)
	fmt.Println("Email : ", mail, "    Error : ", err)
	if resp.Body != nil {
		resB, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			fmt.Println("resp error ", err)
		}
		fmt.Println("Response ", string(resB))
	}
	fmt.Println("======================================================================")
}

func main_1() {

	path := "/pan/pandi/time.txt"

	aray := strings.Split(path, "/")

	path1 := aray[1 : len(aray)-1]
	name := aray[len(aray)-1 : len(aray)]

	fmt.Println(aray, aray[0], "sdfdsff", path1, name)

	file, err := os.Open("/Users/pandiyarajaramamoorthy/Downloads/node-v10.16.0.pkg")
	if err != nil {
		fmt.Println("000000000", err)
		os.Exit(1)
	}
	bufferedReader := bufio.NewReader(file)
	// totBufSize := bufferedReader.Size()
	// fmt.Println(totBufSize)
	// lengthData := 0
	// if totBufSize > 1024 {
	// 	if totBufSize%1024 == 0 {
	// 		lengthData = totBufSize / 1024
	// 	} else {
	// 		lengthData = totBufSize / 1024
	// 		lengthData = +1
	// 	}
	// } else {
	// 	lengthData = +1
	// }

	// fmt.Println("Data   ---->   ", lengthData)
	// resp := struct {
	// 	Name string `json:"Name"`
	// 	Hash string `json:"Hash"`
	// }{}
	//btData := make([]byte, 1024)
	//var hashArray []string

	// fr := files.NewReaderFile(strings.NewReader(""))
	// slf := files.NewSliceDirectory([]files.DirEntry{files.FileEntry("", fr)})
	// fileReader := files.NewMultiFileReader(slf, true)

	// offset := 0

	// fstat, err := filecore.WriteFile("/12D3KooWLxQcja6ucN23uhHEGckeRNbGytNZTYJd99R96DxvQExL/kfs_file_api_log2.txt", fileReader.Boundary(), strconv.Itoa(offset), fileReader)
	// if err != nil {
	// 	kiplog.KIPError("", err, nil)
	// 	os.Exit(1)
	// }
	// currentHash := "bafybeif7ztnhq65lumvvtr4ekcwd2ifwgm3awq4zfr3srh462rwyinlb4y" //fstat.Hash
	// fmt.Println("Current Hash Start ", currentHash)
	//var notFirstWrite bool
	//var respByte []byte
	var fileSize int
	chunkSize := 625000
	var contentType string
	btData := make([]byte, chunkSize)
	//625000
	var boundry string
	for {
		n, err := bufferedReader.Read(btData)
		if err != nil {
			if err == io.EOF {
				//fmt.Println(string(btData))
				break
			}
			fmt.Println("Errorr  ====  ", err)
			os.Exit(1)
		}

		fmt.Println("Read  byte ", n)

		fr := files.NewReaderFile(bytes.NewReader(btData))
		slf := files.NewSliceDirectory([]files.DirEntry{files.FileEntry("", fr)})
		fileReader := files.NewMultiFileReader(slf, true)
		//offset = fileSize
		//fstat, err := filecore.AddFile("/12D3KooWLxQcja6ucN23uhHEGckeRNbGytNZTYJd99R96DxvQExL/kfs_file_api_log7.txt", fileReader.Boundary(), fileReader)
		if fileSize == 0 {
			boundry = fileReader.Boundary()
			contentType = http.DetectContentType(btData)
		}
		//fmt.Println(boundry, fileReader)
		if n > 0 {
			//fmt.Println("  data  ", string(btData))
			fstat, err := filecore.WriteFile("/12D3KooWLxQcja6ucN23uhHEGckeRNbGytNZTYJd99R96DxvQExL/Downloads/node-v10.16.2.pkg", boundry, strconv.Itoa(fileSize), fileReader)
			if err != nil {
				kiplog.KIPError("111111", err, nil)
				os.Exit(1)
			} else {
				fmt.Println("content type ====== ", contentType)

				fmt.Println("====================================================================", fstat.Hash, fstat.Size, fstat.Blocks)
				fileSize = fileSize + chunkSize
				fmt.Println("file size ", fileSize)
				fileSize = fileSize + 1
				fmt.Println("file size ", fileSize)
			}
		}

		// respByte, err := filecore.AppendData(currentHash, fileReader.Boundary(), fileReader)
		// if err != nil {
		// 	fmt.Println("Error appending parts  ======    ", err)
		// }

		// //fmt.Println(readLen)
		// //fmt.Println("=====================================   ", string(btData))
		// fr := files.NewReaderFile(bytes.NewReader(btData))
		// slf := files.NewSliceDirectory([]files.DirEntry{files.FileEntry("", fr)})
		// fileReader := files.NewMultiFileReader(slf, true)

		// respByte, err := filecore.AddFile("", fileReader.Boundary(), fileReader)
		// if err != nil {
		// 	fmt.Println("Error adding parts  ======    ", err)
		// }
		// if err = json.Unmarshal(respByte, &resp); err != nil {
		// 	fmt.Println("Marshel error   ", err)
		// 	os.Exit(1)
		// }
		// hashArray = append(hashArray, resp.Hash)
		//notFirstWrite = true
	}

	fr := files.NewReaderFile(bytes.NewReader(btData))
	slf := files.NewSliceDirectory([]files.DirEntry{files.FileEntry("", fr)})
	fileReader := files.NewMultiFileReader(slf, true)

	//offset := fileSize
	//fstat, err := filecore.AddFile("/12D3KooWLxQcja6ucN23uhHEGckeRNbGytNZTYJd99R96DxvQExL/kfs_file_api_log7.txt", fileReader.Boundary(), fileReader)
	fstat, err := filecore.WriteFile("/12D3KooWLxQcja6ucN23uhHEGckeRNbGytNZTYJd99R96DxvQExL/Downloads/node-v10.16.2.pkg", boundry, strconv.Itoa(fileSize+1), fileReader)
	if err != nil {
		kiplog.KIPError("222222", err, nil)
		os.Exit(1)
	}
	fmt.Println("====================================================================", fstat.Hash, fstat.Size, fstat.Blocks)

	// fr := files.NewReaderFile(strings.NewReader(""))
	// slf := files.NewSliceDirectory([]files.DirEntry{files.FileEntry("", fr)})
	// fileReader := files.NewMultiFileReader(slf, true)

	// offset := 0

	// fstat, err := filecore.WriteFile("/12D3KooWLxQcja6ucN23uhHEGckeRNbGytNZTYJd99R96DxvQExL/kfs_file_api_log3.txt", fileReader.Boundary(), strconv.Itoa(offset), fileReader)
	// if err != nil {
	// 	kiplog.KIPError("111111", err, nil)
	// 	os.Exit(1)
	// }
	// currentHash := fstat.Hash
	// for i, hashData := range hashArray {
	// 	resBt, err := filecore.LinkData(currentHash, "part-"+strconv.Itoa(i), hashData)
	// 	if err != nil {
	// 		kiplog.KIPError("222222222", err, nil)
	// 		os.Exit(1)
	// 	}
	// 	fmt.Println(string(resBt))
	// 	if err = json.Unmarshal(resBt, &resp); err != nil {
	// 		fmt.Println("Marshel error   ", err)
	// 		os.Exit(1)
	// 	}
	// 	currentHash = resp.Hash
	// }
	// fmt.Println(currentHash)

	fmt.Println("No errror  ")
	// for i := 0; i < 5; i++ {
	// 	resp, err := bufferedReader.Peek(1256)
	// 	if err != nil {
	// 		fmt.Println(err)
	// 		os.Exit(1)
	// 	}
	// 	fmt.Println("Peeeeeeeeek   ------>    ", string(resp))
	// }

	// don := make(chan bool)
	// for i := 1; i < 20; i++ {
	// 	go func() {
	// 		respByte, err := filecore.ReadFile("/12D3KooWLxQcja6ucN23uhHEGckeRNbGytNZTYJd99R96DxvQExL/concuTest.txt")
	// 		if err != nil {
	// 			fmt.Println("failed to read file ", err)
	// 			os.Exit(1)
	// 		}
	// 		fr := files.NewReaderFile(bytes.NewReader(randomString(21)))
	// 		slf := files.NewSliceDirectory([]files.DirEntry{files.FileEntry("", fr)})
	// 		fileReader := files.NewMultiFileReader(slf, true)

	// 		offset := len(respByte)

	// 		fstat, err := filecore.WriteFile("/12D3KooWLxQcja6ucN23uhHEGckeRNbGytNZTYJd99R96DxvQExL/concuTest.txt", fileReader.Boundary(), strconv.Itoa(offset), fileReader)
	// 		if err != nil {
	// 			kiplog.KIPError("", err, nil)
	// 			os.Exit(1)
	// 		}
	// 		fmt.Println(fstat.Hash)
	// 	}()
	// }
	// <-don
}

func randomString(len int) []byte {
	bytes := make([]byte, len)
	for i := 0; i < len; i++ {
		bytes[i] = byte(65 + rand.Intn(25)) //A=65 and Z = 65+25
	}
	bytes = append(bytes, []byte("\n")...)
	return bytes //string(bytes)
}

func main() {
	http.HandleFunc("/do", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Hello, %q", html.EscapeString(r.URL.Path))
		ctx := context.Background()
		for i := 0; i < 3; i++ {
			_, err := RPush("numbers", strconv.Itoa(i))
			if err != nil {
				panic(err)
			}
			go test(ctx, i)
		}

	})
	fmt.Println("started 8088...")
	log.Fatal(http.ListenAndServe(":8088", nil))
}

func test(ctx context.Context, i int) {
	fmt.Println("GO ROutiens :  ", runtime.NumGoroutine())
	if sessionList.get(strconv.Itoa(i)) == true {
		fmt.Println("session already exist pushing it to queue", i)
		_, err := RPush("numbers", strconv.Itoa(i))
		if err != nil {
			panic(err)
		}
		return
	}
	sessionList.set(strconv.Itoa(i))
	fmt.Println("created new session ", i)
	cctx, cancelFunc := context.WithCancel(ctx)
	processIt(cctx, cancelFunc, i)
	select {
	case <-cctx.Done():
		fmt.Println("Exiting", cctx.Err())
		sessionList.release(strconv.Itoa(i))
		return
	}
}

func processIt(ctx context.Context, cancelFunc context.CancelFunc, i int) {
	defer cancelFunc()
	fmt.Println("session in use  ", i)
	time.Sleep(time.Second * 3)
}

var (
	sessionList *SessionMap
	cOnce       sync.Once
)

type SessionMap struct {
	m map[string]bool
	l *sync.RWMutex
}

func newSessionList() *SessionMap {

	cOnce.Do(func() {
		sessionList = &SessionMap{
			l: new(sync.RWMutex),
			m: make(map[string]bool),
		}
	})
	return sessionList
}

func init() {
	sessionList = newSessionList()
}

func (c *SessionMap) set(key string) {
	c.l.Lock()
	defer c.l.Unlock()
	c.m[key] = true
}

func (c *SessionMap) release(key string) {
	c.l.Lock()
	defer c.l.Unlock()
	c.m[key] = false
}

func (c *SessionMap) get(key string) bool {
	c.l.RLock()
	defer c.l.RUnlock()
	isBlocked, ok := c.m[key]
	if !ok {
		return false
	}
	return isBlocked
}

func connect() *redis.Client {
	redisclient := redis.NewClient(&redis.Options{
		Addr:            "localhost:6379",
		Password:        "",
		DB:              0,
		MaxRetries:      5,
		MaxRetryBackoff: time.Duration(5 * time.Second),
		PoolTimeout:     time.Duration(3 * time.Second),
	})
	redisclient.Ping().Name()
	return redisclient
}

func LPop(listName string) (string, error) {
	c := connect()
	defer c.Close()
	strCmd := c.LPop(listName)
	return strCmd.Result()
	//fmt.Println(res, err)
}

func RPush(listName, value string) (int64, error) {
	c := connect()
	defer c.Close()
	strCmd := c.RPush(listName, value)
	return strCmd.Result()
	//fmt.Println(res, err)
}

func mainEncy() {
	//sendPostRequest("http://localhost:5001/", "/Users/pandiyarajaramamoorthy/kfs_file_api_log2.txt", "text/plain")

	size := flag.Int("bitsize", ed25519.PrivateKeySize, "select the bitsize of the key to generate")
	typ := flag.String("type", "Ed25519", "select type of key to generate (RSA or Ed25519)")

	flag.Parse()

	var atyp int
	switch strings.ToLower(*typ) {
	case "rsa":
		atyp = crypto.RSA
	case "ed25519":
		atyp = crypto.Ed25519
	default:
		fmt.Fprintln(os.Stderr, "unrecognized key type: ", *typ)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Generating a %d bit %s key...\n", *size, *typ)
	priv1, pub1, err := crypto.GenerateKeyPair(crypto.Ed25519, 2048) //ed25519.GenerateKey(crand.Reader)
	p1k, err := priv1.Raw()
	fmt.Println(len(p1k), err)
	b1k, err := pub1.Raw()
	fmt.Println(len(b1k), err)
	pub, priv, err := ed25519.GenerateKey(crand.Reader)
	crypto.GenerateKeyPair(atyp, *size)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "Success!")

	// pkByt, err := priv.Seed()
	// if err != nil {
	// 	panic(err)
	// }
	// //seedData := ed25519.PrivateKey(pkByt) //.Seed()

	// pbByt, err := pub.Seed()
	// if err != nil {
	// 	panic(err)
	// }

	pkByt := priv //.Seed()

	pbByt := pub

	fmt.Println("Hexa code  :  ", hex.EncodeToString(priv))

	// publicKey, privateKey := new([32]byte), new([64]byte)
	// copy((*publicKey)[:], ed25519.PublicKey(pub))
	// copy((*privateKey)[:], ed25519.PrivateKey(priv))

	// //signedByt := ed25519.Sign(priv, []byte("hi"))
	// var out []byte
	// databt, _ := hex.DecodeString("hi")
	// signedByt := sign.Sign(nil, databt, privateKey)

	// fmt.Println(string(signedByt))

	// respByte, isSuccess := sign.Open(out, signedByt, publicKey)
	// if !isSuccess {
	// 	fmt.Println("out data -------->  ", respByte)
	// 	panic(respByte)
	// }

	// fmt.Println("Response here ======>  ", string(respByte), out)

	fmt.Println("Length    ::::    ", len(pkByt), len(pbByt))

	nonce := make([]byte, 12)
	if _, err := io.ReadFull(crand.Reader, nonce); err != nil {
		panic(err)
	}
	//bytStr := []byte("@icum$n")

	res := append(priv, nonce...)

	fmt.Println("NONCE ========   ", hex.EncodeToString(priv)+hex.EncodeToString(nonce))

	fmt.Println("Response Data  ", hex.EncodeToString(res))

	resStr := hex.EncodeToString(res)

	bytD, err := hex.DecodeString(resStr)
	if err != nil {
		panic(err)
	}
	backupPriKey := bytD[:64]
	backNonce := bytD[64:]
	fmt.Println("comparing  key and nonce  :  ", bytes.Compare(backupPriKey, priv), bytes.Compare(backNonce, nonce))

	ciphertext, err := aesEncrypt(pbByt, nonce, []byte("QmNXqeD38z9FtbYnnFRHyHVUW9mwdFFLs26cNA8eQivo2H"))
	if err != nil {
		kiplog.KIPError("", err, nil)
		panic(err)
	}

	ciphertext1, err := aesEncrypt(pbByt, nonce, []byte("bafybeifnrzziik7ricnpgsnhgkvgs7pldcnu2b2h3jo6w6gavx6qrvv7oa"))
	if err != nil {
		kiplog.KIPError("", err, nil)
		panic(err)
	}

	fmt.Println("Encrypted Data ", hex.EncodeToString(ciphertext), hex.EncodeToString(ciphertext1))

	dat, err := aesDecrypt(pbByt, backNonce, ciphertext)
	if err != nil {
		panic(err)
	}

	dat1, err := aesDecrypt(pbByt, backNonce, ciphertext1)
	if err != nil {
		panic(err)
	}

	fmt.Println("Base 64    ======   ", base64.StdEncoding.EncodeToString(dat), base64.StdEncoding.EncodeToString(dat1))
	fmt.Println("Decrypted Data ", string(dat), string(dat1))

	priK, err := crypto.UnmarshalEd25519PrivateKey(pkByt)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		panic(err)
	}

	pubK, err := crypto.UnmarshalEd25519PublicKey(pbByt)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		panic(err)
	}

	pid, err := peer.IDFromPublicKey(pubK)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		panic(err)
	}

	strID := pid.String()
	fmt.Fprintf(os.Stderr, "ID for String key: %s \n", strID)

	// pid1, err := peer.IDB58Decode(pid.Pretty())
	// if err != nil {
	// 	fmt.Fprintf(os.Stderr, "error  Here ---    %s \n", err.Error())
	// 	os.Exit(1)
	// }

	// pubKey, err := pid1.ExtractPublicKey()
	// if err != nil {
	// 	fmt.Fprintf(os.Stderr, "error   %s \n", err.Error())
	// 	os.Exit(1)
	// }

	// fmt.Fprintf(os.Stderr, "key Compare ----->   %v \n", crypto.KeyEqual(pub, pubKey))

	// fmt.Fprintf(os.Stderr, "ID for generated key: %s \n", pid.Pretty())

	bKey, err := priK.Raw()

	fmt.Println(" Marshalled private key ", len(bKey), bytes.Compare(bKey, pkByt))
	mac := hmac.New(sha512.New, bKey)
	data := mac.Sum(nil)
	fmt.Fprintf(os.Stderr, "key ----->   %x \n", data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error   %s \n", err.Error())
		os.Exit(1)
	}

	fmt.Println("Next Lengte  ::::   ", len(bKey))
	mac.Write([]byte(pid.Pretty()))

	stri := hex.EncodeToString(data)

	fmt.Fprintf(os.Stderr, "key Encoded----->   %s \n", hex.EncodeToString(data))

	db, err := hex.DecodeString(stri)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error   %s \n", err.Error())
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "key Equal  ----->   %v \n", hmac.Equal(data, db))

	str := hex.EncodeToString(bKey)

	fmt.Fprintf(os.Stderr, "%s \n", str)

	byt, err := hex.DecodeString(str)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error  DecodeString  %s \n", err.Error())
		os.Exit(1)
	}

	privKey1, err := crypto.UnmarshalPrivateKey(byt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error  UnmarshalPrivateKey  %s \n", err.Error())
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "key Compare ----->   %v \n", crypto.KeyEqual(priK, privKey1))

	// data, err := priv.Raw()
	// fmt.Fprintf(os.Stderr, "key ----->   %s \n", data)
	// fmt.Fprintf(os.Stderr, "key ----->   %s \n", string(data))
	// if err != nil {
	// 	fmt.Fprintf(os.Stderr, "error   %s \n", err.Error())
	// 	os.Exit(1)
	// }

	// os.Stdout.Write(data)

	// if len(os.Args) < 4 {
	// 	fmt.Println("Please send proper parameters")
	// 	os.Exit(1)
	// }
	// metro.GetETATime(os.Args[1], os.Args[2], os.Args[3])
}

func aesEncrypt(pByt, nonce, data []byte) ([]byte, error) {
	cipherBlock, err := aes.NewCipher(pByt)
	if err != nil {
		return nil, err
	}
	aedGCM, err := cipher.NewGCM(cipherBlock)
	if err != nil {
		return nil, err
	}
	fmt.Println(nonce, data)
	return aedGCM.Seal(nil, nonce, data, nil), nil
}

func aesDecrypt(pByt, nonce, data []byte) ([]byte, error) {
	cipherBlock, err := aes.NewCipher(pByt)
	if err != nil {
		return nil, err
	}
	aedGCM, err := cipher.NewGCM(cipherBlock)
	if err != nil {
		return nil, err
	}
	//nonceSize := aedGCM.NonceSize()
	//nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	//fmt.Println(" nonce and ciphertext ", nonceSize, nonce, ciphertext)
	return aedGCM.Open(nil, nonce, data, nil)
}

func main4() {
	// cer, err := tls.LoadX509KeyPair("server.crt", "server.key")
	// if err != nil {
	// 	log.Println(err)
	// 	return
	// }
	// Certificates: []tls.Certificate{cer}

	// input := struct {
	// 	ID    int    `json:"_id"`
	// 	Value string `json:"value"`
	// }{}
	inpData := struct {
		DBName   string      `json:"dbName"`
		DBType   string      `json:"DBType"`
		Function string      `json:"function"`
		Data     interface{} `json:"data"`
	}{}
	// dataByte, err := readdataFromFile()
	// if err != nil {
	// 	fmt.Println(err)
	// 	os.Exit(2)
	// }
	now := time.Now()
	fmt.Println("Start Time   ----->  ", now.String())
	fmt.Println("\n")
	fmt.Println("\n")
	//for i := 1; i <= 1000; i++ {
	//input.ID = i
	//input.Value = string(dataByte)

	//inpData.DBName = "panDocStor"
	inpData.DBName = "ApplicationStatusList"
	inpData.DBType = "docstore"
	//inpData.Function = "add"
	inpData.Function = "findAll"
	//inpData.Data = input
	inp := struct {
		Prop   string `json:"propname"`
		Comp   string `json:"comp"`
		Values string `json:"values"`
	}{"application_hash", "all", "*"}
	inpData.Data = inp

	bytIn, err := json.Marshal(inpData)
	if err != nil {
		fmt.Println(err)
		os.Exit(2)
	}

	fmt.Println(" Data ----> ", string(bytIn))

	// transCfg := &http.Transport{
	// 	TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // ignore expired SSL certificates
	// }

	client := &http.Client{} //{Transport: transCfg}
	//http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	//inp := []byte(`{"create":"true","type":"docstore"}`)
	req, err := http.NewRequest("POST", "http://165.22.48.231:3003/kfs-data-api", bytes.NewReader(bytIn))
	if err != nil {
		fmt.Println(err)
		os.Exit(2)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("pandi", "123") //Header.Set("Authorization", "Basic cGFuZGk6MTIz")

	data, err := client.Do(req) //Post("http://localhost:3050/orbit", "application/json", bytes.NewReader(bytIn)) //("https://golang.org/")
	if err != nil {
		fmt.Println(err)
	}
	byteData, err := ioutil.ReadAll(data.Body)
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println("Data -------> ", string(byteData))
	//fmt.Println("Data -------> ", i, "  ", string(byteData))
	//}
	fmt.Println("\n")
	fmt.Println("\n")
	endTime := time.Now()
	fmt.Println("Start Time   ----->  ", now.String())
	fmt.Println("End Time   ----->  ", endTime.String())
	fmt.Println("Duration in seconds ----->  ", time.Now().Sub(now).Seconds())
	//newfileUploadRequest("file", "/Users/pandiyarajaramamoorthy/kfs_file_api_log.txt")
	//sendPostRequest("http://localhost:50001", "/Users/pandiyarajaramamoorthy/kfs_file_api_log2.txt", "text/plain")
	//Where your local node is running on localhost:5001

	// userData, err := readdataFromFile()
	// if err != nil {
	// 	fmt.Println(fmt.Sprintf("failed to read user data %+v", err))
	// 	os.Exit(2)
	// }

	// sh := shell.NewShell("localhost:5001")
	// var files []string
	// root := "/Users/pandiyarajaramamoorthy/tst/"
	// err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
	// 	files = append(files, path)
	// 	return nil
	// })
	// if err != nil {
	// 	panic(err)
	// }
	// for _, file := range files {
	// 	fmt.Println("file  ", file)
	// 	byt, err := ioutil.ReadFile(file)
	// 	if err != nil {
	// 		fmt.Println("error reading file :", err)
	// 		//os.Exit(2)
	// 	}

	// 	cid, err := sh.Add(strings.NewReader(string(byt)))
	// 	if err != nil {
	// 		fmt.Fprintf(os.Stderr, "error: %s", err)
	// 		os.Exit(1)
	// 	}
	// 	fmt.Println("added :", cid)
	//}

	// userList, err := getUserList(string(userData))
	// if err != nil {
	// 	fmt.Println(fmt.Sprintf("failed to decode user data %+v", err))
	// 	os.Exit(2)
	// }

	// login(userList)
	//uploadTest()
}
func getUserList(jsonData string) ([]User, error) {
	//var machineList []Machine
	var user []User
	fmt.Println("jsonData ----> ", jsonData)
	err := json.NewDecoder(strings.NewReader(jsonData)).Decode(&user)
	return user, err
}

func readdataFromFile() ([]byte, error) {
	return ioutil.ReadFile("/Users/pandiyarajaramamoorthy/Downloads/MOCK_DATA-2.json")
}

func login(userList []User) {
	// for _, user := range userList {
	// 	resp, err := http.Get("localhost:3000/setkey?user=" + user.UserName + "&password=" + user.Password)
	// 	if err != nil {
	// 		fmt.Println(fmt.Sprintf("failed to decode user data %+v", err))
	// 	}
	// }
}

func post(key string) {

	// Prepare a form that you will submit to that URL.
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	var fw io.Writer
	var err error
	// Add an image file
	// Add other fields
	if fw, err = w.CreateFormField(key); err != nil {
		return
	}
	var r io.Reader
	if _, err := io.Copy(fw, r); err != nil {
		return
	}

	// Don't forget to close the multipart writer.
	// If you don't close it, your request will be missing the terminating boundary.
	w.Close()

	// Now that you have a form, you can submit it to your handler.
	req, err := http.NewRequest("POST", "http://localhost:3000/uploa", &b)
	if err != nil {
		return
	}
	// Don't forget to set the content type, this will contain the boundary.
	req.Header.Add("content-type", "multipart/form-data; boundary=----WebKitFormBoundary7MA4YWxkTrZu0gW")
	req.Header.Set("Content-Type", "multipart/form-data")
	req.Header.Set("Authorization", "Basic cGFuZGk6MTIz")
	var client *http.Client

	// Submit the request
	res, err := client.Do(req)
	if err != nil {
		return
	}

	// Check the response
	if res.StatusCode != http.StatusOK {
		err = fmt.Errorf("bad status: %s", res.Status)
	}
	return

	//http.Post("http://localhost:3000/upload", "multipart/form-data")
}

func uploadTest() {

	///Users/pandiyarajaramamoorthy/kfs_file_api_log.txt
	url := "http://localhost:3000/upload"

	payload := strings.NewReader("------WebKitFormBoundary7MA4YWxkTrZu0gW\r\nContent-Disposition: form-data; name=\"file\"; filename=\"kfs_file_api_log.txt\"\r\nContent-Type: text/plain\r\n\r\n\r\n------WebKitFormBoundary7MA4YWxkTrZu0gW--")

	req, _ := http.NewRequest("POST", url, payload)

	req.Header.Add("content-type", "multipart/form-data; boundary=----WebKitFormBoundary7MA4YWxkTrZu0gW")
	req.Header.Add("Content-Type", "multipart/form-data")
	req.Header.Add("Authorization", "Basic cGFuZGk6MTIz")
	res, _ := http.DefaultClient.Do(req)

	defer res.Body.Close()
	body, _ := ioutil.ReadAll(res.Body)

	fmt.Println(res)
	fmt.Println(string(body))
}

func sendPostRequest(url string, filename string, filetype, path string) []byte {
	fmt.Println("File Name ========================================   ", filename)
	file, err := os.Open(filename)

	if err != nil {
		panic(err)
		log.Fatal("Error opening file : ", err)
	}
	defer file.Close()

	// byteData, err := ioutil.ReadAll(file)
	// if err != nil {
	// 	log.Fatal("Error reading file : ", err)
	// }

	// fmt.Println("Read Data ", string(byteData))

	// body := bytes.NewBuffer(byteData)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filepath.Base(file.Name()))

	if err != nil {
		log.Fatal(err)
	}

	err = writer.WriteField("fileName", path)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("File Data ", string(body.Bytes()))

	io.Copy(part, file)
	writer.Close()
	//url = url + "api/v0/files/write?arg=/pontiya/log.txt&create=true&parents=true&cid-version=1"
	request, err := http.NewRequest("POST", url, body)

	if err != nil {
		log.Fatal(err)
	}

	request.Header.Add("Content-Type", writer.FormDataContentType())
	request.Header.Add("Content-Type", "multipart/form-data")
	request.Header.Add("Authorization", "Basic cGFuZGk6MTIz")
	request.Header.Add("api_key", "4295f9f9a3ff91d6fcdfd8e1a34fc426af69affba9d414d3c209f4d96ef2a6f43f473581a83a231c0485b10fe2bd3eed5e644fdb2b9df3837893ad5ccede794589eae1bad053b6f91e25a0d92fb24eb6d28e723cfd0afeb31dff07e4b134874c897a8229a12caf488e873a15f99df900decab90e1f7ff94c9a8389157b716250")
	request.Header.Add("api_secret", "MIICeAIBADANBgkqhkiG9w0BAQEFAASCAmIwggJeAgEAAoGBAOBxfHbftBxQnMPLT1S2diIJdV3WE0V7ueNjHlEMXqK2883ba4BOwgZ+jY6QKHIsQ6X8GjYCkB7ZsJ6JdzaLEzRekvhU4aJEPPEHe8NwmzS516FPa3q7JfmcB1QjYNdVVINZZ0VJ6zp34NPnTCZ8KDkcQ2bvrvpvuu7+lkuTTHyVAgMBAAECgYAxdZDB+WYNX05Mbz8aIeNCeOceOJCinTNHgo4puhoYrUxortOvwKtNFxJGuknPbyWxLC7ye/oackpThWN554fhdst60dbfR0TH1syn5vnaamhrqk7yWzQc+UIp3rNqa1gtZvc2BbzS/161pe6oataerG6sMT/onOWYUoOeccC0AQJBAOhZAigE0WfRxa5SG102poLzb1g8yJV5Jc9Ke9ZB0MW9in/fBK5wtdTgxQ6cogJfAmX7MZsfWYvxTV1qy2WdooECQQD3Sn05fS581q0TPgdy1ZZ4W1AoaiMqfP/OmLL9giFHQbta/hz/Hpz0FaFb2miMYUbS+XVAfkt4pcSDNsHq+igVAkEAuDA/TlwraOLZk8xRFv7I10yFuuxMknm8aGyCaSI5j1gnYCD6hBKjgoNAk8nFgJ2yuAd+lpsukIqUqvaLER36gQJBAObNOCki4/OSLcFK4IrWPHUizKKbxSyPs/Uv4cbn4IVwHRxlFc0q1lSdp5diNrfmxsJ8H2pNNcVp+gp5Xe4hAq0CQQC2tjrIeRd2TxCyfltT1+KM86HG7+ybi4oDjnT52UsMaBUNJuA24jTM8iKodtkPmUmURnc9J5rFpqlsNaO2l+xq")
	client := &http.Client{}

	response, err := client.Do(request)

	if err != nil {
		log.Fatal(err)
	}
	defer response.Body.Close()

	content, err := ioutil.ReadAll(response.Body)

	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Response -------> ", string(content))

	return content
}

// Creates a new file upload http request with optional extra params
func newfileUploadRequest(paramName, path string) {
	file, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	fileContents, err := ioutil.ReadAll(file)
	if err != nil {
		log.Fatal(err)
	}
	fi, err := file.Stat()
	if err != nil {
		log.Fatal(err)
	}
	file.Close()

	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile(paramName, fi.Name())
	if err != nil {
		log.Fatal(err)
	}
	part.Write(fileContents)

	err = writer.Close()
	if err != nil {
		log.Fatal(err)
	}

	request, err := http.NewRequest("POST", "http://localhost:3000/upload", body)
	if err != nil {
		log.Fatal(err)
	}
	client := &http.Client{}
	resp, err := client.Do(request)
	if err != nil {
		log.Fatal(err)
	} else {
		var bodyContent []byte
		fmt.Println(resp.StatusCode)
		fmt.Println(resp.Header)
		resp.Body.Read(bodyContent)
		resp.Body.Close()
		fmt.Println(bodyContent)
	}
}
