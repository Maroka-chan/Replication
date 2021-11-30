package main

import (
	"bytes"
	"context"
	pb "example.com/Replication/repService"
	"flag"
	"github.com/hashicorp/serf/serf"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"log"
	"math/rand"
	"net"
	"os"
	"time"
)

type ReplicationServer struct {
	pb.UnimplementedReplicationServer
}

var (
	leader        serf.Member
	localAddr     net.IP
	serfCluster   *serf.Serf
	highestBid          = make(chan int32, 1)
	highestBidder int64 = 0
	clients       []int64
)

func main() {
	nodeName := os.Getenv("NODE_NAME")
	caddr := os.Getenv("CLUSTER_ADDRESS")
	highestBid <- 0
	flag.Parse()
	cluster, clustErr := SetupCluster(nodeName, caddr)
	defer cluster.Leave()
	if clustErr != nil {
		log.Fatal(clustErr)
	}
	serfCluster = cluster
	localAddr = serfCluster.LocalMember().Addr
	server := grpc.NewServer()
	pb.RegisterReplicationServer(server, &ReplicationServer{})

	lis, servErr := net.Listen("tcp", ":8080")
	if servErr != nil {
		log.Fatalf("failed to listen: %v", servErr)
	}

	log.Printf("grpc server listening at %v", lis.Addr())
	go func() {
		if err := server.Serve(lis); err != nil {
			log.Fatalf("failed to serve: %v", err)
		}
	}()

	go checkClusterStatus()

	select {}
}

func checkClusterStatus() {
	for {
		for _, member := range serfCluster.Members() {
			log.Printf("%s: [ %s ] (%v)", member.Name, member.Addr.String(), member.Status)
		}
		time.Sleep(time.Second * 2)
	}
}

func SetupCluster(nodeName string, clusterAddr string) (*serf.Serf, error) {
	conf := serf.DefaultConfig()
	conf.Init()
	conf.NodeName = nodeName

	cluster, serfErr := serf.Create(conf)
	if serfErr != nil {
		return nil, errors.Wrap(serfErr, "Couldn't create cluster")
	}

	_, joinErr := cluster.Join([]string{clusterAddr}, true)
	if joinErr != nil {
		log.Printf("Couldn't join cluster, starting own: %v\n", joinErr)
	}
	return cluster, nil
}

func ForwardBid(ctx context.Context, slip *pb.BidSlip) {
	for {
		if leader.Addr.Equal(serfCluster.LocalMember().Addr) {
			for _, member := range serfCluster.Members() {
				if member.Addr.Equal(localAddr) {
					continue
				}
				sendBid(member.Addr, ctx, slip)
			}
			break
		} else if leader.Status == 1 {
			sendBid(leader.Addr, ctx, slip)
			break
		} else {
			reelect()
		}
	}
}

func sendBid(ip net.IP, ctx context.Context, slip *pb.BidSlip) {
	var conn, err2 = grpc.Dial(ip.String()+":8080", grpc.WithInsecure(), grpc.WithBlock())
	if err2 != nil {
		log.Fatalf("did not connect: %v", err2)
	}
	var client = pb.NewReplicationClient(conn)
	_, err := client.Bid(ctx, slip)
	if err != nil {
		return
	}
	conn.Close()
}

func (s *ReplicationServer) Bid(ctx context.Context, bidSlip *pb.BidSlip) (*pb.Response, error) {
	if !Contains(clients, bidSlip.Id) {
		clients = append(clients, bidSlip.Id)
	}
	var res pb.Response
	curHighestBid := <-highestBid
	if curHighestBid < bidSlip.Amount {
		highestBidder = bidSlip.Id
		highestBid <- bidSlip.Amount
		res = pb.Response{Res: pb.ResponseStatus_SUCCESS}
	} else {
		highestBid <- curHighestBid
		res = pb.Response{Res: pb.ResponseStatus_FAIL}
	}

	receiverIp, ok := ctx.Value("ip").(*net.IP)
	if ok && !leader.Addr.Equal(*receiverIp) {
		var ctx2 = context.WithValue(context.Background(), "ip", serfCluster.LocalMember().Addr)
		ForwardBid(ctx2, bidSlip)
	}
	return &pb.Response{Res: res.Res}, nil
}

func Contains(cl []int64, id int64) bool {
	for _, v := range cl {
		if v == id {
			return true
		}
	}
	return false
}

func (s *ReplicationServer) Result(ctx context.Context, _ *pb.Empty) (*pb.BidSlip, error) {
	bid := <-highestBid
	hb := highestBidder
	highestBid <- bid
	return &pb.BidSlip{Id: hb, Amount: bid}, nil
}

func (s *ReplicationServer) Elect(ctx context.Context, candidate *pb.Candidate) (*pb.Response, error) {
	log.Printf("Electing new leader! %s", candidate.Ip)
	candidateIp := net.ParseIP(candidate.Ip)
	if bytes.Compare(candidateIp, localAddr) >= 0 {
		for _, member := range serfCluster.Members() {
			if member.Addr.Equal(localAddr) {
				continue
			}
			if member.Addr.Equal(candidateIp) {
				leader = member
				log.Printf("New leader elected! %s", candidate.Ip)
			}
		}
		return &pb.Response{Res: pb.ResponseStatus_SUCCESS}, nil
	}
	return &pb.Response{Res: pb.ResponseStatus_FAIL}, nil
}

func reelect() {
	log.Printf("Electing new leader! %s:  %s", serfCluster.LocalMember().Name, localAddr)
	for _, member := range serfCluster.Members() {
		if member.Addr.Equal(localAddr) {
			continue
		}

		ctx, _ := context.WithTimeout(context.Background(), 1*time.Second)
		clientConn, err := grpc.DialContext(ctx, member.Addr.String()+":8080", grpc.WithInsecure(), grpc.WithBlock())
		if err != nil {
			log.Fatalf("did not connect: %v", err)
			return
		}

		var client = pb.NewReplicationClient(clientConn)
		response, err := client.Elect(ctx, &pb.Candidate{Ip: localAddr.String()})
		if err != nil {
			return
		}
		if response.Res == pb.ResponseStatus_FAIL {
			log.Printf("Found node with higher IP! %s:  %s", member.Name, member.Addr.String())
			v := rand.Intn(900) + 100
			time.Sleep(time.Millisecond * time.Duration(v))
			return
		}
		clientConn.Close()
	}
	leader = serfCluster.LocalMember()
}
