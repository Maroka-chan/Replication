package main

import (
	"context"
	pb "example.com/Replication/repService"
	"fmt"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"log"
	"time"
)

var (
	address = "localhost"
	ports   = []int{50001, 50002, 50003, 50004, 50005, 50006, 50007, 50008, 50009}
)

var (
	id        = uuid.New()
	bid int32 = 1
)

func main() {
	ctx, conn, conerr := Connect()
	var client pb.ReplicationClient
	if conerr != nil {
		log.Fatalf("could not connect to a server: %v", conerr)
	}

	defer conn.Close()
	for {
		client = pb.NewReplicationClient(conn)
		var res, err = client.Result(ctx, &pb.Empty{})
		if err != nil {
			ctx2, conn2, errc := Connect()
			if errc != nil {
				log.Fatalf("could not get result: %v", err)
			}
			conn = conn2
			ctx = ctx2
			continue
		}
		if res.Id != int64(id.ID()) {
			log.Printf("id %v did not match own id, %v", res.Id, id.ID())
			var status, errr = client.Bid(ctx, &pb.BidSlip{Id: int64(id.ID()), Amount: res.Amount + 1})
			if errr != nil {
				ctx3, conn3, err3 := Connect()
				if err3 != nil {
					log.Fatalf("something went wrong: %v", errr)
				}
				conn = conn3
				ctx = ctx3
				continue
			}
			if status.Res == pb.ResponseStatus_SUCCESS {
				log.Printf("succesfully bid with: %v", res.Amount+1)
			} else if status.Res == pb.ResponseStatus_FAIL {
				log.Printf("failed bid with: %v", res.Amount+1)
			}
		}
		time.Sleep(time.Millisecond * 100)
	}
}

func Connect() (context.Context, *grpc.ClientConn, error) {
	var conn *grpc.ClientConn
	var connErr error
	var timeoutCtx context.Context

	for i := range ports {
		timeoutCtx, _ = context.WithTimeout(context.Background(), 1*time.Second)
		conn, connErr = grpc.DialContext(timeoutCtx, fmt.Sprintf("%s:%d", address, ports[i]), grpc.WithInsecure(), grpc.WithBlock())
		if connErr != nil {
			log.Printf("Connection failed %d: %v", ports[i], connErr)
			continue
		}
		break
	}
	return timeoutCtx, conn, connErr
}
