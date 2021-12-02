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
	reelect()
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

func ForwardBid(ip net.IP, ctx context.Context, slip *pb.BidSlip) (*pb.Response, error) {
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	timeoutConn, err := grpc.DialContext(timeoutCtx, ip.String()+":8080", grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		log.Println("Dial failed!")
		return &pb.Response{}, err
	}
	var client = pb.NewReplicationClient(timeoutConn)
	res, err := client.Bid(ctx, slip)
	if err != nil {
		return &pb.Response{}, err
	}
	timeoutConn.Close()
	return res, nil
}

func (s *ReplicationServer) Bid(ctx context.Context, bidSlip *pb.BidSlip) (*pb.Response, error) {
	var response *pb.Response
	for {
		if leader.Addr.Equal(localAddr) {
			response = setBid(bidSlip)
			break
		} else if leader.Status == 1 {
			receiverIp, ok := ctx.Value("ip").(*net.IP)
			if ok && !leader.Addr.Equal(*receiverIp) {
				timeoutCtx, _ := context.WithTimeout(context.Background(), 2*time.Second)
				var ctx2 = context.WithValue(timeoutCtx, "ip", localAddr)
				res, err := ForwardBid(leader.Addr, ctx2, bidSlip)
				if err != nil {
					return response, err
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
	return &pb.Response{Res: res.Res}
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
		if leader.Addr.Equal(localAddr) {
			bid := <-highestBid
			hb := highestBidder
			highestBid <- bid
			return &pb.BidSlip{Id: hb, Amount: bid}, nil
		} else if leader.Status == 1 {
			return getResult(leader.Addr, ctx)
		} else {
			reelect()
		}
	}
}

func getResult(ip net.IP, ctx context.Context) (*pb.BidSlip, error) {
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	timeoutConn, err := grpc.DialContext(timeoutCtx, ip.String()+":8080", grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		log.Println("Dial failed!")
		return &pb.BidSlip{}, err
	}
	var client = pb.NewReplicationClient(timeoutConn)
	res, err2 := client.Result(ctx, &pb.Empty{})
	if err2 != nil {
		return &pb.BidSlip{}, err
	}
	timeoutConn.Close()
	return res, nil
}

func reelect() {
	newLeader := serf.Member{Addr: net.IPv4(0, 0, 0, 0)}
	for _, member := range serfCluster.Members() {
		if member.Status == 1 && bytes.Compare(newLeader.Addr, member.Addr) >= 0 {
			newLeader = member
		}
	}
	leader = newLeader
}
