// tcpState
//
// V2
// collect letters

// V3 
// refactor loop to allow for peeks

package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"strings"
//	"io"
	"os"
	"os/exec"
//	"os/signal"
//	"syscall"

	"github.com/creack/pty"
//	"golang.org/x/term"

	util "github.com/prr123/utility/utilLib"
)

// Telnet Protocol Constants
const (
	TelIAC  = 255
	TelDONT = 254
	TelDO   = 253
	TelWONT = 252
	TelWILL = 251
	TelSB   = 250 // Sub-negotiation Begin
	TelSE   = 240 // Sub-negotiation End
	TelECHO = 1
	TelNAWS = 31  // Negotiate About Window Size
	TelTERM = 24
	TelSPEED = 32
	TelLine = 34
	TelENV = 39
)

var dbg bool

// Protocol State Enum
type ProtocolState int
const (
	StateNormal ProtocolState = iota
	StateIAC
	StateNegotiate
	StateSB          // Inside a Sub-negotiation block
	StateSBRx        // Inside Sub-negotiation, awaiting final SE
)

var States = [5]string{"StateNormal", "StateIAC", "StateNegotiate", "StateSB", "StateSBRx"}

type ClientSession struct {
	conn          net.Conn
	protocolState ProtocolState
	currentCmd    byte
	currentOption byte
	sbBuffer      []byte // Temporary buffer for sub-negotiation data
	inputBuffer   strings.Builder
	spty *os.File
	
	// Terminal Dimensions
	WindowWidth  uint16
	WindowHeight uint16
}

func NewClientSession(conn net.Conn) *ClientSession {

	cmd := exec.Command("/bin/bash", "-i")

	// Start the command with a pty.
	ptmx, err := pty.Start(cmd)
    if err != nil {log.Fatalf("error ptyStart: %v\n",err)}

	// Make sure to close the pty at the end.
//  	defer func() { _ = ptmx.Close() }() // Best effort.

	return &ClientSession{
		conn:          conn,
		protocolState: StateNormal,
		sbBuffer:      make([]byte, 0, 128),
		spty: ptmx,
	}
}

func (s *ClientSession) WriteString(msg string) {
	s.conn.Write([]byte(msg))
}

func (s *ClientSession) SendNegotiation(cmd, option byte) {
	s.conn.Write([]byte{TelIAC, cmd, option})
}

func (s *ClientSession) HandleSession() {
	defer s.conn.Close()
  	defer func() { _ = s.spty.Close() }() // Best effort.

	// Initial Handshake: Request Remote Echo AND Ask for Window Size updates
	s.SendNegotiation(TelDONT, TelECHO)
	s.SendNegotiation(TelDO, TelNAWS)

	s.WriteString("Welcome! Resize your terminal to test NAWS.\r\n> ")

	reader := bufio.NewReader(s.conn)
	for {
		b, err := reader.ReadByte()
		if err != nil {
			if dbg {log.Printf(" read error: %v\n", err)}
			return
		}
		if b == '\r' {
			b2, err := reader.ReadByte()
			if err != nil {
				if dbg {log.Printf(" read LF error: %v\n", err)}
				return
			}
			if b2 != '\n' {
				if dbg {log.Printf(" missing LF error\n")}
				return
			}
			b= '\n'
		}

		s.processProtocolByte(b)
	}
}

// Protocol State Machine with Sub-negotiation handling
func (s *ClientSession) processProtocolByte(b byte) {

	st := int(s.protocolState)
	if dbg {log.Printf("Proc Prot State: %s %d\n", States[st], b)}
	switch s.protocolState {
	case StateNormal:
		switch b {
		case TelIAC:
			s.protocolState = StateIAC
		case '\r','\n': 
			line := s.inputBuffer.String()
//dbg
		if dbg {log.Printf("line [%d]: %s\n", len(line), line)}

			s.inputBuffer.Reset()
			s.handleCommand(line)

		default: 
			s.conn.Write([]byte{b}) // Echo
			s.inputBuffer.WriteByte(b)
		}

	case StateIAC:
		if dbg {log.Printf("rec cmd: %d\n", b)}
		switch b {
		case TelDO, TelDONT, TelWILL, TelWONT:
			s.currentCmd = b
			s.protocolState = StateNegotiate
		case TelSB:
			s.sbBuffer = s.sbBuffer[:0] // Clear buffer
			s.protocolState = StateSB
		case TelSE:
			s.currentCmd = TelSE
			s.protocolState = StateNormal
		default:
			if dbg {log.Printf("unknown cmd rec: %d\n", b)}
//			s.protocolState = StateNormal
		}

	case StateNegotiate:
		s.currentOption = b
		if dbg {fmt.Printf("[Neg] Cmd: %d Opt: %d\n", s.currentCmd, s.currentOption)}
		s.protocolState = StateNormal

	case StateSB:
		// The first byte of SB tells us which option this sub-negotiation belongs to
		s.currentOption = b
		s.protocolState = StateSBRx

	case StateSBRx:
		// We are accumulating parameters until we see IAC SE
		if b == TelIAC {
			// Sneak peek or escape check: if next is SE, we finish.
			// For simplicity in a basic stream, we assume next control character logic:
//			s.protocolState = StateNormal // Temporarily pop out to check next character
			s.protocolState = StateIAC
		} else {
			s.sbBuffer = append(s.sbBuffer, b)
		}

		// If we popped out out of StateSBRx because of an IAC, the next loop execution 
		// handles the action. Let's make sure it handles SE safely:
	default:
		log.Fatalf("error invalid state: %d\n", st)
	}
	
	// Quick intercept to handle the trailing edge of Sub-negotiation (IAC SE)
//	if s.protocolState == StateNormal && b == TelSE {
//		s.parseSubnegotiation()
//	}
}

// Parse collected sub-negotiation parameters
func (s *ClientSession) parseSubnegotiation() {
	if dbg {
		log.Printf("SubNeg Opt: %d >%d: %v\n",  s.currentOption, len(s.sbBuffer), s.sbBuffer)
	}
	switch s.currentOption {
	case TelNAWS:
		// NAWS payloads must contain exactly 4 bytes (2 bytes width, 2 bytes height)
		if len(s.sbBuffer) == 4 {
			s.WindowWidth = (uint16(s.sbBuffer[0]) << 8) | uint16(s.sbBuffer[1])
			s.WindowHeight = (uint16(s.sbBuffer[2]) << 8) | uint16(s.sbBuffer[3])
			// Print confirmation to server logs
			fmt.Printf("[NAWS] Terminal resized to: %d x %d\n", s.WindowWidth, s.WindowHeight)
		}
	}
}

func (s *ClientSession) handleCommand(inp string) {

	inp = strings.TrimSpace(inp)
	out := make([]byte,1024)
	switch inp {
	case "size":
		s.WriteString(fmt.Sprintf("Your current terminal size is: %d columns x %d rows.\r\n> ", s.WindowWidth, s.WindowHeight))
	case "exit":
		s.conn.Close()
	default:
		inp2:= inp + "\n"
		if dbg {log.Printf("inp: %s", inp2)}
		ln, err := s.spty.WriteString(inp2)
		if err != nil {log.Fatalf("error -- write to pty: %v\n", err)}
		if dbg {log.Printf("write pty %d: %s", ln, inp2)}

		n, err := s.spty.Read(out)
		if err != nil {log.Fatalf("error handle Cmd Read: %v\n", err)}
//		if dbg {log.Printf("  handle Cmd %d: %s %v\n", n, out[:n], out[:n])}
		if dbg {log.Printf("  handle Cmd %d: %s\n", n, out[:n])}
		s.conn.Write(out[:n])

//		s.WriteString("Commands: size, exit\r\n ")
	}
}

func main() {

   numArgs := len(os.Args)

    flags:=[]string{"dbg","port", "serv"}

    useStr := "/serv=adr /port=<portnum> [/dbg]"
    helpStr := fmt.Sprintf("help: tcp server V2\n")

    if numArgs > len(flags)+1 {
        fmt.Println("too many arguments in cl!")
        fmt.Println("usage: %s %s\n", os.Args[0], useStr)
        os.Exit(1)
    }

   if numArgs == 1 {
		fmt.Printf("usage is: %s\n", useStr)
		fmt.Printf("%s\n", helpStr)
        os.Exit(1)
    }

   if numArgs == 2 {
        if os.Args[1] == "help" {
            fmt.Printf("usage is: %s\n", useStr)
            fmt.Printf("%s\n", helpStr)
            os.Exit(1)
        }
    }


    flagMap, err := util.ParseFlags(os.Args, flags)
    if err != nil {log.Fatalf("util.ParseFlags: %v\n", err)}

	dbg = false
    _, ok := flagMap["dbg"]
    if ok {dbg = true}

    portStr := ""
    pval, ok := flagMap["port"]
    if ok {
        if pval.(string) == "none" {log.Fatalf("error -- no port number provided with /port flag!")}
        portStr = pval.(string)
    }

    sadrStr := ""
    sval, ok := flagMap["serv"]
    if ok {
        if sval.(string) == "none" {log.Fatalf("error -- no serve IP provided with /serve flag!")}
        sadrStr = sval.(string)
    }

    servAdr := sadrStr + ":" + portStr
    if dbg {
        fmt.Printf(" serv IP:  %s\n", sadrStr)
        fmt.Printf(" port:     %s\n", portStr)
        fmt.Printf(" serv Adr: %s\n", servAdr)
    }


    listener, err := net.Listen("tcp", servAdr)
    if err != nil {log.Fatalf("Failed to start server: %v", err)}
	defer listener.Close()
	log.Printf("Telnet NAWS Server listening on %s ...\n", servAdr)

	for {
		conn, _ := listener.Accept()
		session := NewClientSession(conn)
		go session.HandleSession()
	}
}
