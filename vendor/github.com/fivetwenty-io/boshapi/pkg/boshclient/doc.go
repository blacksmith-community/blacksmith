// Package boshclient provides the main entry point for creating BOSH Director API clients.
//
// Use boshclient.New to create a new client with automatic UAA endpoint discovery:
//
//	ctx := context.Background()
//	cli, err := boshclient.New(ctx, &boshapi.Config{
//	    DirectorURL:  "https://bosh.example.com:25555",
//	    ClientID:     "admin",
//	    ClientSecret: "secret",
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	info, err := cli.GetInfo(ctx)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	fmt.Println(info.Name)
package boshclient
