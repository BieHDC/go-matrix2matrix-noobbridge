package main

import (
	"flag"
	"gopkg.in/yaml.v2"
	"html"
	"log"
	"maunium.net/go/mautrix"
	event "maunium.net/go/mautrix/event"
	id "maunium.net/go/mautrix/id"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

//fixme possible commands
//	try rejoin rooms (maybe after kick, or it timed out)
//	maybe cross fed ban? who is authorised? generally meant to be a same-person managed implementation, so any admin
//	kick other room out (spam attack), removing self is a simple as kicking or banning the botuser
//		and it would attempt to rejoin after a reboot

type NamedRoom struct {
	Name                string    `yaml:"name"`          //rooms with the same name are bridged together
	Room                id.RoomID `yaml:"room"`          //the roomid it is on the particular server
	EnableMediaInbound  bool      `yaml:"mediainbound"`  //allow media being bridged into this room
	EnableMediaOutbound bool      `yaml:"mediaoutbound"` //allow media being bridged out of this room
}

type Server struct {
	Homeserver              string      `yaml:"homeserver"`
	LoginType               string      `yaml:"login_type"` // "password" or "accesstoken"
	Username_or_Userid      string      `yaml:"username_or_userid"`
	Password_or_Accesstoken string      `yaml:"password_or_accesstoken"`
	Rooms                   []NamedRoom `yaml:"rooms"`

	//internal
	client *mautrix.Client // the client connection to a homeserver
}

type BotConfiguration struct {
	Servers []*Server `yaml:"servers"`

	// Bot settings
	DeviceDisplayName string `yaml:"device_display_name"`
	UniqueDeviceID    string `yaml:"unique_device_id"`
}

func (config *BotConfiguration) Parse(data []byte) error {
	return yaml.UnmarshalStrict(data, config)
}

// Make sure we can exit cleanly
var closechannel = make(chan os.Signal, 1)
var bot_is_quitting = false

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	configPath := flag.String("config", "", "config.yaml file location")
	flag.Parse()
	if *configPath == "" {
		log.Printf("Usage of %s:", os.Args[0])
		flag.PrintDefaults()
		os.Exit(1)
	}

	log.Printf("Reading config file %s", *configPath)
	configYaml, err := os.ReadFile(*configPath)
	if err != nil {
		log.Panic(err)
	}

	botconfig := BotConfiguration{}
	err = botconfig.Parse(configYaml)
	if err != nil {
		log.Panic(err)
	}

	for _, server := range botconfig.Servers {
		server := server //just to make sure due to closures
		if server.Homeserver == "" {
			log.Panic("Empty Homeserver URL")
		}
		if server.Username_or_Userid == "" || server.Password_or_Accesstoken == "" {
			log.Panic("Invalid login data for", server.Homeserver)
		}
		if len(server.Rooms) == 0 {
			log.Panic(server.Homeserver, "has no rooms in his list")
		}

		if server.LoginType == "password" {
			log.Println("Logging into", server.Homeserver, "as", server.Username_or_Userid)
			server.client, err = mautrix.NewClient(server.Homeserver, "", "")
			if err != nil {
				log.Panic(err)
			}

			serverreply, err := server.client.Login(&mautrix.ReqLogin{
				Type:                     mautrix.AuthTypePassword,
				Identifier:               mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: server.Username_or_Userid},
				Password:                 server.Password_or_Accesstoken,
				StoreCredentials:         true,
				DeviceID:                 id.DeviceID(botconfig.UniqueDeviceID),
				InitialDeviceDisplayName: botconfig.DeviceDisplayName,
			})
			if err != nil {
				log.Panic(err)
			}

			log.Printf("Consider using Access token based login:\n\tUserID:%s\n\tAccessToken:%s\n", serverreply.UserID, serverreply.AccessToken)
			log.Println("Login successful to", server.Homeserver)
		} else if server.LoginType == "accesstoken" {
			server.client, err = mautrix.NewClient(server.Homeserver, id.UserID(server.Username_or_Userid), server.Password_or_Accesstoken)
			if err != nil {
				log.Panic(err)
			}
			server.client.DeviceID = id.DeviceID(botconfig.UniqueDeviceID)
			log.Println("Login successful to", server.Homeserver)
		} else {
			log.Panic("Unknown login type ", server.LoginType)
		}

		resp, err := server.client.Whoami()
		if err != nil {
			log.Panic(err)
		}
		our_uid := resp.UserID.String()

		for _, room := range server.Rooms {
			// join the room if we are not and report if we are banned
			_, err := server.client.JoinRoomByID(room.Room)
			if err != nil {
				//fixme if we got 429, try again later
				log.Println(server.Homeserver, "-> failed to join room >", room.Room.String(), "< with reason", err.Error())
			}
		}

		syncer := server.client.Syncer.(*mautrix.DefaultSyncer)
		ignoreoldevents := mautrix.OldEventIgnorer{UserID: server.client.UserID}
		ignoreoldevents.Register(syncer)

		syncer.OnEventType(event.EventMessage, func(source mautrix.EventSource, evt *event.Event) {
			// Ignore messages from ourselves
			// this also means you cant use your main account as a bridge
			if evt.Sender == server.client.UserID {
				return
			}
			// This prints every message to your console, if you need to check around
			// fmt.Printf("<%[1]s> %[4]s (%[2]s/%[3]s)(room>>%[5]s<<)(%[6]d)\n", evt.Sender, evt.Type.String(), evt.ID, evt.Content.AsMessage().Body, evt.RoomID, evt.Timestamp)

			var targetroom NamedRoom
			for _, room := range server.Rooms {
				if room.Room == evt.RoomID {
					targetroom = room
					break
				}
			}
			if targetroom.Room == id.RoomID("") {
				return //not a room we care about
			}

			msgevt := evt.Content.AsMessage()

			if msgevt.RelatesTo != nil {
				// Force the fallback if anything releations is going on
				msgevt.Format = ""
				msgevt.FormattedBody = ""
			}
			msgevt.NewContent = nil //strip out fancy replace, fallback will do it for us
			msgevt.RelatesTo = nil  //strip out any releation stuff

			switch msgevt.MsgType {
			case event.MsgText, event.MsgNotice, event.MsgLocation:
				msgevt.Body = evt.Sender.String() + " (from Bridge):\n" + msgevt.Body
				if msgevt.FormattedBody != "" {
					msgevt.FormattedBody = html.EscapeString(evt.Sender.String()) + " (from Bridge):<br>" + msgevt.FormattedBody
				}
				for _, serveri := range botconfig.Servers {
					for _, room := range serveri.Rooms {
						if room.Name == targetroom.Name {
							if room.Room != targetroom.Room {
								serveri.client.SendMessageEvent(room.Room, event.EventMessage, msgevt)
							}
						}
					}
				}

			case event.MsgEmote, event.MsgImage, event.MsgVideo, event.MsgAudio, event.MsgFile:
				// if this room is marked to not bridge media outside, we return
				if !targetroom.EnableMediaOutbound {
					return
				}

				//copy msgevt
				fullmsgevt := *msgevt
				//copy msgevt.Info
				hasthumbnail := false
				var fullinfo event.FileInfo
				if msgevt.Info != nil && msgevt.Info.ThumbnailURL != "" {
					fullinfo = *msgevt.Info
					hasthumbnail = true
				}

				//stage 1 - source server
				contentFile, err := parseAndDownload(server, fullmsgevt.URL)
				if err != nil {
					log.Println(server.Homeserver, "-> Failed to download media:", fullmsgevt.Body)
					return //nothing to do
				}

				var contentThumbnail []byte
				if hasthumbnail {
					var err error
					contentThumbnail, err = parseAndDownload(server, fullmsgevt.Info.ThumbnailURL)
					if err != nil {
						log.Println(server.Homeserver, "-> Failed to download thumbnail for:", fullmsgevt.Body)
						return //why would this fail if the other one did not?
					}
				}

				//stage 2 - destination server
				//fixme we should/could probably make this parallel, since upload can take a while
				//might also beg the question to put this in a mutex to keep the order of traffic
				//however i fear a giant clog building up
				for _, serveri := range botconfig.Servers {
					var localFile *id.ContentURIString
					var localThumbnail *id.ContentURIString

					for _, room := range serveri.Rooms {
						if room.Name == targetroom.Name {
							if room.Room != targetroom.Room {
								// if the room does not allow incoming media, move to the next one
								if !room.EnableMediaInbound {
									continue
								}

								if serveri == server {
									serveri.client.SendText(room.Room, evt.Sender.String()+" (from Bridge): "+msgevt.Body)
									serveri.client.SendMessageEvent(room.Room, event.EventMessage, fullmsgevt)
									continue //quick path if we are on the same server
								}

								if localFile == nil {
									resupload, err := serveri.client.UploadBytes(contentFile, fullmsgevt.Info.MimeType)
									if err != nil {
										log.Println(serveri.Homeserver, "-> Failed to upload file for:", fullmsgevt.Body, "with error:", err.Error())
										continue //what do you want me to do about it?
									}
									resuploadstring := resupload.ContentURI.CUString()
									localFile = &resuploadstring
								}
								if hasthumbnail && localThumbnail == nil {
									resupload, err := serveri.client.UploadBytes(contentThumbnail, fullmsgevt.Info.ThumbnailInfo.MimeType)
									if err != nil {
										log.Println(serveri.Homeserver, "-> Failed to upload thumbnail for:", fullmsgevt.Body, "with error:", err.Error())
										continue //what do you want me to do about it?
									}
									resuploadstring := resupload.ContentURI.CUString()
									localThumbnail = &resuploadstring
								}

								copyoffullmsgevt := fullmsgevt
								copyoffullmsgevt.URL = *localFile

								if hasthumbnail {
									copyoffullinfo := fullinfo
									copyoffullinfo.ThumbnailURL = *localThumbnail
									copyoffullmsgevt.Info = &copyoffullinfo
								}

								serveri.client.SendText(room.Room, evt.Sender.String()+" (from Bridge): "+msgevt.Body)
								serveri.client.SendMessageEvent(room.Room, event.EventMessage, copyoffullmsgevt)
							}
						}
					}
				}

			default:
				log.Println("Unhandled Message Event:", string(msgevt.MsgType))
				return
			}
		})

		syncer.OnEventType(event.StateMember, func(source mautrix.EventSource, evt *event.Event) {
			if *evt.StateKey == our_uid && evt.Content.AsMember().Membership.IsLeaveOrBan() {
				var newrooms []NamedRoom
				for _, room := range server.Rooms {
					if room.Room != evt.RoomID {
						newrooms = append(newrooms, room)
					}
				}
				server.Rooms = newrooms
				log.Println(server.Homeserver, "-> Left or banned from", evt.RoomID.String())
			}
		})
	}

	// Once all servers are connected and ready and the rooms connected, start processing events
	for _, server := range botconfig.Servers {
		server := server //just to make sure due to closures
		go func(server *Server) {
			for {
				err = server.client.Sync()
				if err != nil {
					log.Println(server.Homeserver, "-> Sync Error:", err.Error())
				}
				if bot_is_quitting {
					break
				}
			}
		}(server)
	}

	// Make sure we can exit cleanly
	signal.Notify(closechannel,
		os.Interrupt,
		os.Kill,
		syscall.SIGABRT,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGQUIT,
		syscall.SIGTERM,
	)

	<-closechannel
	log.Println("Shutting down...")
	bot_is_quitting = true

	var shutdowns sync.WaitGroup
	for _, server := range botconfig.Servers {
		server := server //just to make sure due to closures
		shutdowns.Add(1)
		go func(server *Server) {
			defer shutdowns.Done()
			server.client.StopSync()
			time.Sleep(1 * time.Second)
			server.client.Client.CloseIdleConnections()
		}(server)
	}
	go func() {
		shutdowns.Wait()
		closechannel <- os.Interrupt
	}()

	//force kill on hang or waitgroup is done
	<-closechannel
	os.Exit(0)
}

func parseAndDownload(server *Server, uri id.ContentURIString) ([]byte, error) {
	parsed, err := uri.Parse()
	if err != nil {
		return nil, err
	}

	content, err := server.client.DownloadBytes(parsed) //we need the bytes since we might do multiple uploads
	if err != nil {
		return nil, err
	}

	return content, nil
}
