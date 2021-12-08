package main

import (
	"bytes"
	"context"
	pb "example.com/Replication/repService"
	"flag"
	"github.com/hashicorp/serf/serf"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"log"
	"net"
	"os"
	"time"
)

type ReplicationServer struct {
	pb.UnimplementedReplicationServer
}

const port = "8080"

var (
	leaderAddr    net.IP
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
	reelect()
	server := grpc.NewServer()
	pb.RegisterReplicationServer(server, &ReplicationServer{})

	lis, servErr := net.Listen("tcp", ":"+port)
	if servErr != nil {
		log.Fatalf("failed to listen: %v", servErr)
	}

	log.Printf("grpc server listening at %v", lis.Addr())
	go func() {
		if err := server.Serve(lis); err != nil {
			log.Fatalf("failed to serve: %v", err)
		}
	}()

	select {}
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

func ForwardBid(ip net.IP, slip *pb.BidSlip) (*pb.Response, error) {
	timeoutCtx, _ := context.WithTimeout(context.Background(), 2*time.Second)
	ctx2 := metadata.AppendToOutgoingContext(timeoutCtx, "ip", localAddr.String())
	timeoutConn, err := grpc.DialContext(ctx2, ip.String()+":"+port, grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		log.Printf("Dial failed: %v", err)
		return &pb.Response{Res: pb.ResponseStatus_EXCEPTION}, err
	}
	var client = pb.NewReplicationClient(timeoutConn)
	res, err2 := client.Bid(ctx2, slip)
	if err2 != nil {
		log.Printf("Forwarding bid failed [%s]: %v", ip.String(), err2)
		return &pb.Response{Res: pb.ResponseStatus_EXCEPTION}, err2
	}
	timeoutConn.Close()
	return res, nil
}

func (s *ReplicationServer) Bid(ctx context.Context, bidSlip *pb.BidSlip) (*pb.Response, error) {
	var response *pb.Response
	var receiverIp net.IP

	md, ok := metadata.FromIncomingContext(ctx)
	if ok && len(md.Get("ip")) > 0 {
		receiverIp = net.ParseIP(md.Get("ip")[0])
	}

	for {
		if leaderAddr.Equal(localAddr) {
			if (ok && localAddr.Equal(receiverIp)) || (ok && leaderAddr.Equal(receiverIp)) {
				return &pb.Response{Res: pb.ResponseStatus_EXCEPTION}, errors.New("Received Bid from self")
			}
			for _, member := range serfCluster.Members() {
				if member.Status == 1 && !member.Addr.Equal(localAddr) {
					log.Printf("Forwarding to [%s]", member.Addr)
					ForwardBid(member.Addr, bidSlip)
				}
			}
			response = setBid(bidSlip)
			break
		} else if leaderOnline() {
			if ok && leaderAddr.Equal(receiverIp) {
				response = setBid(bidSlip)
			} else {
				log.Printf("FORWARD TO LEADER [%s]", leaderAddr)
				res, err := ForwardBid(leaderAddr, bidSlip)
				if err != nil {
					return &pb.Response{Res: pb.ResponseStatus_EXCEPTION}, err
				}
				response = res
			}
			break
		} else {
			reelect()
		}
	}
	return response, nil
}

func setBid(bidSlip *pb.BidSlip) *pb.Response {
	if !Contains(clients, bidSlip.Id) {
		clients = append(clients, bidSlip.Id)
	}
	var res *pb.Response
	curHighestBid := <-highestBid
	if curHighestBid < bidSlip.Amount {
		highestBidder = bidSlip.Id
		highestBid <- bidSlip.Amount
		res = &pb.Response{Res: pb.ResponseStatus_SUCCESS}
	} else {
		highestBid <- curHighestBid
		res = &pb.Response{Res: pb.ResponseStatus_FAIL}
	}
	return res
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
	for {
		if leaderAddr.Equal(localAddr) {
			bid := <-highestBid
			hb := highestBidder
			highestBid <- bid
			return &pb.BidSlip{Id: hb, Amount: bid}, nil
		} else if leaderOnline() {
			slip, err := getResult(leaderAddr, ctx)
			if err != nil {
				continue
			}
			return slip, nil
		} else {
			reelect()
		}
	}
}

func getResult(ip net.IP, ctx context.Context) (*pb.BidSlip, error) {
	timeoutCtx, _ := context.WithTimeout(context.Background(), 2*time.Second)
	timeoutConn, err := grpc.DialContext(timeoutCtx, ip.String()+":"+port, grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		log.Printf("Dial failed: %v", err)
		return &pb.BidSlip{Amount: 0, Id: 0}, err
	}
	var client = pb.NewReplicationClient(timeoutConn)
	res, err2 := client.Result(ctx, &pb.Empty{})
	if err2 != nil {
		return &pb.BidSlip{Amount: 0, Id: 0}, err
	}
	timeoutConn.Close()
	return res, nil
}

func leaderOnline() bool {
	for _, member := range serfCluster.Members() {
		if member.Addr.Equal(leaderAddr) {
			return member.Status == 1
		}
	}
	return false
}

func reelect() {
	newLeader := serf.Member{Addr: net.IPv4(0, 0, 0, 0)}
	for _, member := range serfCluster.Members() {
		if member.Status == 1 && bytes.Compare(member.Addr, newLeader.Addr) >= 0 {
			newLeader = member
		}
	}
	leaderAddr = newLeader.Addr
}
