package main

import (
	"context"
	pb "example.com/Replication/repService"
	"flag"
	"github.com/hashicorp/serf/serf"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"log"
	"net"
	"os"
)

type ReplicationServer struct {
	pb.UnimplementedReplicationServer
}

var (
	localAddr     net.IP
	serfMembers   []serf.Member
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
	localAddr = cluster.LocalMember().Addr
	serfMembers = cluster.Members()

	server := grpc.NewServer()
	pb.RegisterReplicationServer(server, &ReplicationServer{})

	lis, servErr := net.Listen("tcp", ":8080")
	if servErr != nil {
		log.Fatalf("failed to listen: %v", servErr)
	}

	log.Printf("server listening at %v", lis.Addr())
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

func ForwardBid(ctx context.Context, slip *pb.BidSlip) {
	for _, member := range serfMembers {
		if member.Addr.String() == localAddr.String() {
			continue
		}
		var conn, err2 = grpc.Dial(member.Addr.String()+":8080", grpc.WithInsecure(), grpc.WithBlock())
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
	if ctx.Value("forward") != nil {
		var ctx2 = context.Background()
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
