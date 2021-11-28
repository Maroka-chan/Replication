package main

import (
	"context"
	pb "example.com/Replication/repService"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"log"
	"time"
)

var (
	address = "localhost"
	ports = []string{"50001","50002","50003","50004"}
)

var (
	id = uuid.New()
	bid int32 = 1
)

func main() {
	var conn *grpc.ClientConn
	var connErr error

	for i := range ports {
		conn, connErr = grpc.Dial(address + ":" + ports[i], grpc.WithInsecure(), grpc.WithBlock())
		if connErr != nil {
			log.Printf("did not connect: %v", connErr)
			continue
		}
		break
	}
	defer conn.Close()
	var client = pb.NewReplicationClient(conn)

	var ctx = context.WithValue(context.Background(), "forward", "1")
	for {
		var res, err = client.Result(ctx, &pb.Empty{})
		if err != nil {
			log.Fatalf("could not get result: %v", err)
		}
		if res.Id != int64(id.ID()) {
			var status, errr = client.Bid(ctx, &pb.BidSlip{Id: int64(id.ID()), Amount: res.Amount + 1})
			if errr != nil {
				log.Fatalf("something went wrong: %v", errr)
			}
			if status.Res == pb.ResponseStatus_SUCCESS {
				log.Printf("succesfully bid with: %v", res.Amount + 1)
			} else if status.Res == pb.ResponseStatus_FAIL {
				log.Printf("failed bid with: %v", res.Amount + 1)
			}
			time.Sleep(time.Second * 2)
		}

	}
}
