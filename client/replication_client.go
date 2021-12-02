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
	ports   = []string{"8080", "50002", "50003", "50004"}
)

var (
	id        = uuid.New()
	bid int32 = 1
)

func main() {
	ctx, conn, conerr := Connect()
	if conerr != nil {
		log.Fatalf("could not connect to a server: %v", conerr)
	}

	defer conn.Close()
	var client = pb.NewReplicationClient(conn)
	for {
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
		time.Sleep(time.Second * 2)
	}
}

func Connect() (context.Context, *grpc.ClientConn, error) {
	var conn *grpc.ClientConn
	var connErr error

	timeoutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for i := range ports {
		conn, connErr = grpc.DialContext(timeoutCtx, address+":"+ports[i], grpc.WithInsecure(), grpc.WithBlock())
		if connErr != nil {
			log.Printf("did not connect: %v", connErr)
			continue
		}
		break
	}
	return timeoutCtx, conn, connErr
}
