package trigcases

// HostSetup is how the host interfaces put the trigger into a golden's
// state: SCPI messages (spec 11), then web controls on the running type,
// and the registers the golden gives. The simulator's app run (sim area
// trigger) reads them from host.json, which TestHostFile keeps equal to
// Hosts.
type HostSetup struct {
	Name string            `json:"name"`
	SCPI []string          `json:"scpi,omitempty"`
	Web  []WebControl      `json:"web,omitempty"`
	Want map[string]uint16 `json:"want"`
}

// WebControl is one /api/set body: a choice, source or state list in
// Text, a number in Value.
type WebControl struct {
	Control string  `json:"control"`
	Text    string  `json:"text,omitempty"`
	Value   float64 `json:"value"`
}

// Hosts are the host setups of every golden the host interfaces can
// express, in the goldens' order (a golden relies on the registers the
// ones before it left).
func Hosts() []HostSetup {
	var out []HostSetup
	for _, c := range Cases {
		if h, ok := hostSetups[c.Name]; ok {
			h.Name, h.Want = c.Name, c.Want
			out = append(out, h)
		}
	}
	return out
}

// hostSetups set up every golden Cases holds that the host interfaces can
// express, by its name.
var hostSetups = map[string]HostSetup{
	"edge-c1-rising":             {SCPI: []string{":TRIG:EDGE:SOUR C1;SLOP RIS;LEV 0.5", ":TRIG:TYPE EDGE"}},
	"edge-c2-falling-ac-holdoff": {SCPI: []string{":TRIG:EDGE:SOUR C2;SLOP FALL;LEV -1;COUP AC;NREJ ON;HLDT 1.0000003E-6;HOLD TIME"}},
	"edge-acline-either":         {SCPI: []string{":TRIG:EDGE:SOUR LINE;SLOP ALT;LEV 0;COUP DC;NREJ OFF;HOLD OFF"}},
	"edge-holdoff-30s":           {SCPI: []string{":TRIG:EDGE:SOUR C1;SLOP RIS;HLDT 30;HOLD TIME"}},
	"slope-c1-rising-less":       {SCPI: []string{":TRIG:SLOP:SOUR C1;SLOP RIS;HLEV 0.5;LLEV -0.5;LIM LESS;TUPP 20E-9", ":TRIG:TYPE SLOPe"}},
	"pulse-c2-negative-inside":   {SCPI: []string{":TRIG:PULS:SOUR C2;POL NEG;LEV 0.25;LIM INN;TLOW 100E-9;TUPP 1E-6", ":TRIG:TYPE PULSE"}},
	"pulse-c1-positive-greater":  {SCPI: []string{":TRIG:PULS:SOUR C1;POL POS;LEV 0;LIM GREAT;TLOW 1E-6"}},
	"runt-c1-positive": {Web: []WebControl{
		{Control: "trigtype", Text: "runt"},
		{Control: "trig.source", Text: "C1"},
		{Control: "trig.slope", Text: "rising"},
		{Control: "trig.level", Value: 0.15},
		{Control: "trig.level_low", Value: 0.1},
		{Control: "trig.limit", Text: "less"},
		{Control: "trig.upper", Value: 50e-9},
	}},
	"window-c1":                   {SCPI: []string{":TRIG:WIND:SOUR C1;HLEV 0;LLEV -0.833", ":TRIG:TYPE WINDow"}},
	"interval-c1-falling-outside": {SCPI: []string{":TRIG:INT:SOUR C1;SLOP FALL;LEV 0;LIM OUT;TLOW 10E-9;TUPP 2E-6", ":TRIG:TYPE INTerval"}},
	"dropout-c2-rising-edge":      {SCPI: []string{":TRIG:DROP:SOUR C2;SLOP RIS;LEV 0.5;TIME 1E-6;TYPE EDGE", ":TRIG:TYPE DROPout"}},
	"dropout-c2-falling-state":    {SCPI: []string{":TRIG:DROP:SLOP FALL;TIME 20E-9;TYPE STAT"}},
	"nth-edge-c1":                 {SCPI: []string{":TRIG:NEDG:SOUR C1;SLOP RIS;LEV 0;EDGE 5;IDLE 100E-9", ":TRIG:TYPE NEDGe"}},
	"video-ntsc":                  {SCPI: []string{":TRIG:VID:SOUR C1;LEV -0.15;STAN NTSC;SYNC SEL;LINE 10;FIEL 1", ":TRIG:TYPE VIDeo"}},
	"video-720p":                  {SCPI: []string{":TRIG:VID:STAN P720L50;LINE 2"}},
	"video-custom":                {SCPI: []string{":TRIG:VID:STAN CUST;LINE 10;FIEL 3;LCNT 300;FRAT 25Hz;INT 1;FCNT 4"}},
	"pattern-and": {SCPI: []string{
		":TRIG:PATT:INP H,L,X,H;LEV C1,0.5;LEV C2,-0.5;LEV C4,1;LOG AND;LIM GREAT;TLOW 10E-9", ":TRIG:TYPE PATTern",
	}},
	"pattern-nor":     {SCPI: []string{":TRIG:PATT:LOG NOR;TLOW 2E-9"}},
	"qualified-state": {SCPI: []string{":TRIG:QUAL:ES C1;ESL RIS;ELEV 0;QS C2;QLEV 0.5;TYPE STAT,HIGH", ":TRIG:TYPE QUALified"}},
	"qualified-edge-delay": {Web: []WebControl{
		{Control: "trig.source", Text: "C4"},
		{Control: "trig.slope", Text: "falling"},
		{Control: "trig.level", Value: 0},
		{Control: "trig.gate_source", Text: "C3"},
		{Control: "trig.gate_level", Value: -0.5},
		{Control: "trig.gate_form", Text: "edge_delay"},
		{Control: "trig.gate_high", Value: 0},
		{Control: "trig.limit", Text: "inside"},
		{Control: "trig.lower", Value: 10e-9},
		{Control: "trig.upper", Value: 1e-6},
	}},
	"qualified-state-delay": {SCPI: []string{":TRIG:QUAL:ES C1;ESL RIS;ELEV 0;QS C2;QLEV 0;TYPE STATE_DLY,LOW;LIM GREAT;TLOW 50E-9"}},
	"qualified-edge":        {SCPI: []string{":TRIG:QUAL:TYPE EDGE,RIS"}},
	"setup": {SCPI: []string{
		":TRIG:SHOL:TYPE SET;CS C1;CTHR 0;SLOP RIS;DS C2;DTHR 0.5;LEV HIGH;LIM LESS;TUPP 20E-9", ":TRIG:TYPE SHOLd",
	}},
	"hold": {SCPI: []string{":TRIG:SHOL:TYPE HOLD;SLOP FALL;LEV LOW;LIM GREAT;TLOW 5E-9"}},
	"delay-pattern-then-edge": {SCPI: []string{
		":TRIG:DEL:SOUR H,X,X,X;LEV C1,0.5;SLOP RIS;SOUR2 C3;SLOP2 FALL;LEV2 0.05;LIM LESS;TUPP 1E-6", ":TRIG:TYPE DELay",
	}},
	"i2c-addr7-read": {SCPI: []string{
		":TRIG:IIC:SCLS C1;SCLT 0.5;SDAS C2;SDAT 0.5;COND 7ADDR;ADDR 80;RWB READ;DATA 18;DAT2 256", ":TRIG:TYPE IIC",
	}},
	"i2c-eeprom": {SCPI: []string{":TRIG:IIC:COND EEPR;ADDR 128;RWB WRIT;LIM GRE;DATA 64"}},
	"spi-msb": {SCPI: []string{
		":TRIG:SPI:CLKS C1;CLKT 0.5;LATC RIS;MISOS C2;MISOT 0.5;TTYP MISO;NCSS C3;NCST 0.5;CSTY NCS;DLEN 8;DATA 1,0,1,X,0,0,1,1;BIT MSB",
		":TRIG:TYPE SPI",
	}},
	"uart-data": {Web: []WebControl{
		{Control: "trigtype", Text: "uart"},
		{Control: "trig.source", Text: "C1"},
		{Control: "trig.level", Value: 1},
		{Control: "trig.condition", Text: "data"},
		{Control: "trig.baud", Value: 115200},
		{Control: "trig.bits", Value: 8},
		{Control: "trig.parity", Text: "even"},
		{Control: "trig.stop", Value: 1},
		{Control: "trig.idle", Text: "high"},
		{Control: "trig.bit_order", Text: "lsb"},
		{Control: "trig.compare", Text: "equal"},
		{Control: "trig.value", Value: 0x55},
	}},
	"can-id-data": {SCPI: []string{
		":TRIG:CAN:SOUR C2;THR 0.5;COND ID_AND_DATA;BAUD 500kbps;ID 291;IDL 11BITS;DATA 171;DAT2 256", ":TRIG:TYPE CAN",
	}},
	"lin-id": {SCPI: []string{
		":TRIG:LIN:SOUR C1;THR 0.5;COND ID;BAUD 19200bps;ID 34;STAN 1;ERR:CHEC 1;SYNC 1;PAR 1", ":TRIG:TYPE LIN",
	}},
}
