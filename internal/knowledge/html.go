package knowledge

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ExportHTML builds the graph from all notes and writes a fully
// self-contained interactive HTML file (no network, no CDN — works offline).
// Returns the written path plus note/link counts.
func ExportHTML() (string, int, int, error) {
	notes, err := LoadNotes()
	if err != nil {
		return "", 0, 0, err
	}
	g := BuildGraph(notes)
	data, err := g.JSON()
	if err != nil {
		return "", 0, 0, err
	}
	// </script> inside note bodies would terminate the data block early.
	safe := strings.ReplaceAll(string(data), "</", `<\/`)
	html := strings.Replace(graphHTML, "__GRAPH_DATA__", safe, 1)
	path := filepath.Join(Dir(), "graph.html")
	if err := os.WriteFile(path, []byte(html), 0o644); err != nil {
		return "", 0, 0, err
	}
	return path, len(g.Nodes), len(g.Links), nil
}

// OpenInBrowser opens the given file with the OS default browser.
func OpenInBrowser(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", "", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

var graphHTML = fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>@code Knowledge Graph</title>
<style>
  :root{--bg:#0d1017;--panel:#151a24;--line:#2a3245;--text:#d7dce6;--dim:#8a93a6;--accent:#7aa2f7;--ghost:#4a5268}
  *{margin:0;padding:0;box-sizing:border-box}
  body{background:var(--bg);color:var(--text);font:14px/1.5 -apple-system,"Segoe UI",Roboto,sans-serif;overflow:hidden}
  #graph{display:block;cursor:grab}
  #graph.dragging{cursor:grabbing}
  #topbar{position:fixed;top:0;left:0;right:0;display:flex;gap:10px;align-items:center;padding:10px 14px;background:linear-gradient(var(--bg),transparent);z-index:2}
  #topbar h1{font-size:15px;font-weight:600;color:var(--accent);white-space:nowrap}
  #topbar h1 span{color:var(--dim);font-weight:400}
  #search{flex:0 1 300px;background:var(--panel);border:1px solid var(--line);border-radius:8px;color:var(--text);padding:6px 12px;outline:none}
  #search:focus{border-color:var(--accent)}
  #stats{color:var(--dim);font-size:12px;white-space:nowrap}
  #panel{position:fixed;top:0;right:0;bottom:0;width:min(420px,90vw);background:var(--panel);border-left:1px solid var(--line);padding:20px;overflow-y:auto;transform:translateX(100%%);transition:transform .18s ease;z-index:3}
  #panel.open{transform:none}
  #panel h2{color:var(--accent);font-size:17px;margin-bottom:4px}
  #panel .meta{color:var(--dim);font-size:12px;margin-bottom:14px}
  #panel pre{white-space:pre-wrap;word-wrap:break-word;font:13px/1.6 ui-monospace,Menlo,Consolas,monospace;color:var(--text)}
  #panel .close{position:absolute;top:12px;right:14px;background:none;border:none;color:var(--dim);font-size:20px;cursor:pointer}
  #hint{position:fixed;bottom:10px;left:14px;color:var(--dim);font-size:12px;z-index:2}
</style>
</head>
<body>
<div id="topbar">
  <h1>@code <span>Knowledge Graph</span></h1>
  <input id="search" placeholder="Search notes…" autocomplete="off">
  <div id="stats"></div>
</div>
<canvas id="graph"></canvas>
<div id="panel">
  <button class="close" onclick="panel.classList.remove('open')">×</button>
  <h2 id="p-title"></h2>
  <div class="meta" id="p-meta"></div>
  <pre id="p-body"></pre>
</div>
<div id="hint">drag nodes · scroll to zoom · drag background to pan · click a node to read it</div>
<script id="data" type="application/json">__GRAPH_DATA__</script>
<script>
(function(){
"use strict";
const DATA=JSON.parse(document.getElementById('data').textContent);
const canvas=document.getElementById('graph'),ctx=canvas.getContext('2d');
const panel=document.getElementById('panel');window.panel=panel;
const searchEl=document.getElementById('search');
let W,H,dpr=window.devicePixelRatio||1;
function resize(){W=innerWidth;H=innerHeight;canvas.width=W*dpr;canvas.height=H*dpr;canvas.style.width=W+'px';canvas.style.height=H+'px';ctx.setTransform(dpr,0,0,dpr,0,0)}
resize();addEventListener('resize',resize);

// tag palette (Obsidian-ish)
const PALETTE=['#7aa2f7','#9ece6a','#e0af68','#f7768e','#bb9af7','#7dcfff','#ff9e64','#73daca'];
const tagColor={};let ci=0;
const nodes=DATA.nodes.map(n=>{
  if(n.tag&&!(n.tag in tagColor))tagColor[n.tag]=PALETTE[ci++%%PALETTE.length];
  return {...n,x:(Math.random()-0.5)*600,y:(Math.random()-0.5)*600,vx:0,vy:0,
    r:6+Math.min(10,Math.sqrt(n.size||0)*3),
    color:n.ghost?'#4a5268':(n.tag?tagColor[n.tag]:'#7aa2f7')};
});
const byId={};nodes.forEach(n=>byId[n.id]=n);
const links=DATA.links.map(l=>({s:byId[l.source],t:byId[l.target]})).filter(l=>l.s&&l.t);
document.getElementById('stats').textContent=nodes.length+' notes · '+links.length+' links';

// simple force simulation: repulsion + spring + centering
let alpha=1;
function tick(){
  for(let i=0;i<nodes.length;i++)for(let j=i+1;j<nodes.length;j++){
    const a=nodes[i],b=nodes[j];let dx=a.x-b.x,dy=a.y-b.y;let d2=dx*dx+dy*dy||1;
    if(d2<160000){const f=2600/d2;const d=Math.sqrt(d2);dx/=d;dy/=d;
      a.vx+=dx*f;a.vy+=dy*f;b.vx-=dx*f;b.vy-=dy*f;}
  }
  links.forEach(l=>{
    let dx=l.t.x-l.s.x,dy=l.t.y-l.s.y;const d=Math.sqrt(dx*dx+dy*dy)||1;
    const f=(d-110)*0.02;dx/=d;dy/=d;
    l.s.vx+=dx*f;l.s.vy+=dy*f;l.t.vx-=dx*f;l.t.vy-=dy*f;
  });
  nodes.forEach(n=>{
    n.vx-=n.x*0.0025;n.vy-=n.y*0.0025;
    if(n!==dragNode){n.x+=n.vx*alpha;n.y+=n.vy*alpha}
    n.vx*=0.85;n.vy*=0.85;
  });
  if(alpha>0.02)alpha*=0.995;
}

// view transform
let scale=1,ox=0,oy=0;
function toScreen(x,y){return[W/2+ox+x*scale,H/2+oy+y*scale]}
function toWorld(px,py){return[(px-W/2-ox)/scale,(py-H/2-oy)/scale]}

let hoverNode=null,dragNode=null,panning=false,lastX=0,lastY=0,query='';
function matches(n){return !query||n.title.toLowerCase().includes(query)||n.id.includes(query)}

function draw(){
  ctx.clearRect(0,0,W,H);
  ctx.lineWidth=1;
  links.forEach(l=>{
    const dim=query&&!(matches(l.s)||matches(l.t));
    const hot=hoverNode&&(l.s===hoverNode||l.t===hoverNode);
    ctx.strokeStyle=hot?'rgba(122,162,247,0.8)':dim?'rgba(42,50,69,0.25)':'rgba(74,82,104,0.55)';
    const[ax,ay]=toScreen(l.s.x,l.s.y),[bx,by]=toScreen(l.t.x,l.t.y);
    ctx.beginPath();ctx.moveTo(ax,ay);ctx.lineTo(bx,by);ctx.stroke();
  });
  nodes.forEach(n=>{
    const[x,y]=toScreen(n.x,n.y);
    const dim=!matches(n);
    ctx.globalAlpha=dim?0.15:1;
    ctx.beginPath();ctx.arc(x,y,n.r*scale,0,7);
    ctx.fillStyle=n.color;ctx.fill();
    if(n===hoverNode){ctx.strokeStyle='#fff';ctx.lineWidth=2;ctx.stroke();ctx.lineWidth=1}
    if(n.ghost){ctx.strokeStyle='rgba(215,220,230,0.35)';ctx.stroke()}
    if(scale>0.55||n===hoverNode||(query&&!dim)){
      ctx.fillStyle=n===hoverNode?'#fff':'rgba(215,220,230,0.75)';
      ctx.font=(n===hoverNode?'600 ':'')+'12px -apple-system,sans-serif';
      ctx.textAlign='center';ctx.fillText(n.title,x,y+n.r*scale+14);
    }
    ctx.globalAlpha=1;
  });
}
function loop(){tick();draw();requestAnimationFrame(loop)}
loop();

function nodeAt(px,py){
  const[wx,wy]=toWorld(px,py);
  for(let i=nodes.length-1;i>=0;i--){const n=nodes[i];
    const dx=wx-n.x,dy=wy-n.y;if(dx*dx+dy*dy<(n.r+4)*(n.r+4))return n}
  return null;
}
canvas.addEventListener('mousedown',e=>{
  const n=nodeAt(e.clientX,e.clientY);
  if(n){dragNode=n;alpha=Math.max(alpha,0.3)}else{panning=true;canvas.classList.add('dragging')}
  lastX=e.clientX;lastY=e.clientY;
});
addEventListener('mousemove',e=>{
  if(dragNode){const[wx,wy]=toWorld(e.clientX,e.clientY);dragNode.x=wx;dragNode.y=wy;alpha=Math.max(alpha,0.3)}
  else if(panning){ox+=e.clientX-lastX;oy+=e.clientY-lastY}
  else{hoverNode=nodeAt(e.clientX,e.clientY);canvas.style.cursor=hoverNode?'pointer':'grab'}
  lastX=e.clientX;lastY=e.clientY;
});
addEventListener('mouseup',e=>{
  if(dragNode&&Math.abs(e.clientX-lastX)<3){openPanel(dragNode)}
  dragNode=null;panning=false;canvas.classList.remove('dragging');
});
canvas.addEventListener('click',e=>{const n=nodeAt(e.clientX,e.clientY);if(n)openPanel(n)});
canvas.addEventListener('wheel',e=>{
  e.preventDefault();
  const f=e.deltaY<0?1.12:0.89;const ns=Math.min(4,Math.max(0.15,scale*f));
  const[wx,wy]=toWorld(e.clientX,e.clientY);
  ox-=wx*(ns-scale);oy-=wy*(ns-scale);scale=ns;
},{passive:false});
searchEl.addEventListener('input',()=>{query=searchEl.value.trim().toLowerCase()});

function openPanel(n){
  document.getElementById('p-title').textContent=n.title;
  document.getElementById('p-meta').textContent=n.ghost
    ?'ghost node — linked to, but the note does not exist yet'
    :n.id+'.md · '+(n.size||0)+' link'+(n.size===1?'':'s')+(n.tag?' · #'+n.tag:'');
  document.getElementById('p-body').textContent=n.ghost?'':n.body;
  panel.classList.add('open');
}
})();
</script>
</body>
</html>
`)
