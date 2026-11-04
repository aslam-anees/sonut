package graph

import (
	"encoding/json"
	"os"
	"strings"
)

// writeHTML writes a fully self-contained interactive force-directed view of
// the graph (no network, no CDN — works offline). Nodes are colored by
// detected community; click a node to see its connections with origin tags.
func (g *Graph) writeHTML(path string) error {
	data, err := json.Marshal(g)
	if err != nil {
		return err
	}
	// </script> inside labels would terminate the data block early.
	safe := strings.ReplaceAll(string(data), "</", `<\/`)
	html := strings.Replace(graphHTML, "__GRAPH_DATA__", safe, 1)
	return os.WriteFile(path, []byte(html), 0o644)
}

const graphHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>@code Project Graph</title>
<style>
  :root{--bg:#0d1017;--panel:#151a24;--line:#2a3245;--text:#d7dce6;--dim:#8a93a6;--accent:#7aa2f7}
  *{margin:0;padding:0;box-sizing:border-box}
  body{background:var(--bg);color:var(--text);font:14px/1.5 -apple-system,"Segoe UI",Roboto,sans-serif;overflow:hidden}
  #graph{display:block;cursor:grab}
  #topbar{position:fixed;top:0;left:0;right:0;display:flex;gap:10px;align-items:center;padding:10px 14px;background:linear-gradient(var(--bg),transparent);z-index:2}
  #topbar h1{font-size:15px;font-weight:600;color:var(--accent);white-space:nowrap}
  #topbar h1 span{color:var(--dim);font-weight:400}
  #search{flex:0 1 300px;background:var(--panel);border:1px solid var(--line);border-radius:8px;color:var(--text);padding:6px 12px;outline:none}
  #search:focus{border-color:var(--accent)}
  #stats{color:var(--dim);font-size:12px;white-space:nowrap}
  #legend{position:fixed;bottom:34px;left:14px;z-index:2;font-size:12px;color:var(--dim)}
  #legend div{display:flex;align-items:center;gap:6px}
  #legend i{width:10px;height:10px;border-radius:50%;display:inline-block}
  #panel{position:fixed;top:0;right:0;bottom:0;width:min(440px,90vw);background:var(--panel);border-left:1px solid var(--line);padding:20px;overflow-y:auto;transform:translateX(100%);transition:transform .18s ease;z-index:3}
  #panel.open{transform:none}
  #panel h2{color:var(--accent);font-size:17px;margin-bottom:4px;word-break:break-all}
  #panel .meta{color:var(--dim);font-size:12px;margin-bottom:14px}
  #panel ul{list-style:none;font:12px/1.8 ui-monospace,Menlo,Consolas,monospace}
  #panel li b{color:var(--text);font-weight:600}
  #panel .tag{color:var(--dim)}
  #panel .close{position:absolute;top:12px;right:14px;background:none;border:none;color:var(--dim);font-size:20px;cursor:pointer}
  #hint{position:fixed;bottom:10px;left:14px;color:var(--dim);font-size:12px;z-index:2}
</style>
</head>
<body>
<div id="topbar">
  <h1>@code <span>Project Graph</span></h1>
  <input id="search" placeholder="Search nodes…" autocomplete="off">
  <div id="stats"></div>
</div>
<canvas id="graph"></canvas>
<div id="legend"></div>
<div id="panel">
  <button class="close" onclick="panel.classList.remove('open')">×</button>
  <h2 id="p-title"></h2>
  <div class="meta" id="p-meta"></div>
  <ul id="p-edges"></ul>
</div>
<div id="hint">drag nodes · scroll to zoom · drag background to pan · click a node for its connections</div>
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

const PALETTE=['#7aa2f7','#9ece6a','#e0af68','#f7768e','#bb9af7','#7dcfff','#ff9e64','#73daca','#c0caf5','#a6da95'];
const nodes=DATA.nodes.map(n=>({...n,
  x:(Math.random()-0.5)*900,y:(Math.random()-0.5)*900,vx:0,vy:0,
  r:4+Math.min(12,Math.sqrt(n.degree||0)*1.6),
  color:PALETTE[(n.community||0)%PALETTE.length]}));
const byId={};nodes.forEach(n=>byId[n.id]=n);
const links=DATA.edges.map(e=>({s:byId[e.from],t:byId[e.to],rel:e.rel,origin:e.origin})).filter(l=>l.s&&l.t);
document.getElementById('stats').textContent=nodes.length+' nodes · '+links.length+' edges';

const commCount={};nodes.forEach(n=>{commCount[n.community]=(commCount[n.community]||0)+1});
const legend=document.getElementById('legend');
Object.keys(commCount).map(Number).sort((a,b)=>a-b).slice(0,8).forEach(c=>{
  if(commCount[c]<2)return;
  const row=document.createElement('div');
  row.innerHTML='<i style="background:'+PALETTE[c%PALETTE.length]+'"></i> community '+c+' ('+commCount[c]+')';
  legend.appendChild(row);
});

// spatial-grid repulsion keeps large graphs interactive
let alpha=1;
function tick(){
  const cell=140,grid={};
  nodes.forEach(n=>{const k=(n.x/cell|0)+':'+(n.y/cell|0);(grid[k]=grid[k]||[]).push(n)});
  nodes.forEach(a=>{
    const gx=a.x/cell|0,gy=a.y/cell|0;
    for(let i=-1;i<=1;i++)for(let j=-1;j<=1;j++){
      const bucket=grid[(gx+i)+':'+(gy+j)];if(!bucket)continue;
      for(const b of bucket){if(b===a)continue;
        let dx=a.x-b.x,dy=a.y-b.y;const d2=dx*dx+dy*dy||1;
        if(d2<19600){const f=1400/d2,d=Math.sqrt(d2);a.vx+=dx/d*f;a.vy+=dy/d*f}}
    }});
  links.forEach(l=>{
    let dx=l.t.x-l.s.x,dy=l.t.y-l.s.y;const d=Math.sqrt(dx*dx+dy*dy)||1;
    const f=(d-90)*0.02;dx/=d;dy/=d;
    l.s.vx+=dx*f;l.s.vy+=dy*f;l.t.vx-=dx*f;l.t.vy-=dy*f;
  });
  nodes.forEach(n=>{
    n.vx-=n.x*0.002;n.vy-=n.y*0.002;
    if(n!==dragNode){n.x+=n.vx*alpha;n.y+=n.vy*alpha}
    n.vx*=0.85;n.vy*=0.85;
  });
  if(alpha>0.02)alpha*=0.996;
}

let scale=0.7,ox=0,oy=0;
function toScreen(x,y){return[W/2+ox+x*scale,H/2+oy+y*scale]}
function toWorld(px,py){return[(px-W/2-ox)/scale,(py-H/2-oy)/scale]}

let hoverNode=null,dragNode=null,panning=false,lastX=0,lastY=0,query='';
function matches(n){return !query||n.label.toLowerCase().includes(query)||n.id.toLowerCase().includes(query)}

function draw(){
  ctx.clearRect(0,0,W,H);
  ctx.lineWidth=1;
  links.forEach(l=>{
    const dim=query&&!(matches(l.s)||matches(l.t));
    const hot=hoverNode&&(l.s===hoverNode||l.t===hoverNode);
    ctx.strokeStyle=hot?'rgba(122,162,247,0.85)':dim?'rgba(42,50,69,0.2)':(l.origin==='INFERRED'?'rgba(74,82,104,0.3)':'rgba(74,82,104,0.55)');
    const[ax,ay]=toScreen(l.s.x,l.s.y),[bx,by]=toScreen(l.t.x,l.t.y);
    ctx.beginPath();ctx.moveTo(ax,ay);ctx.lineTo(bx,by);ctx.stroke();
  });
  nodes.forEach(n=>{
    const[x,y]=toScreen(n.x,n.y);
    if(x<-30||y<-30||x>W+30||y>H+30)return;
    const dim=!matches(n);
    ctx.globalAlpha=dim?0.12:1;
    ctx.beginPath();ctx.arc(x,y,n.r*scale,0,7);
    ctx.fillStyle=n.color;ctx.fill();
    if(n===hoverNode){ctx.strokeStyle='#fff';ctx.lineWidth=2;ctx.stroke();ctx.lineWidth=1}
    if((scale>0.9&&n.degree>2)||n===hoverNode||(query&&!dim)){
      ctx.fillStyle=n===hoverNode?'#fff':'rgba(215,220,230,0.7)';
      ctx.font=(n===hoverNode?'600 ':'')+'11px -apple-system,sans-serif';
      ctx.textAlign='center';ctx.fillText(n.label,x,y+n.r*scale+12);
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
  if(n){dragNode=n;alpha=Math.max(alpha,0.3)}else{panning=true}
  lastX=e.clientX;lastY=e.clientY;
});
addEventListener('mousemove',e=>{
  if(dragNode){const[wx,wy]=toWorld(e.clientX,e.clientY);dragNode.x=wx;dragNode.y=wy;alpha=Math.max(alpha,0.3)}
  else if(panning){ox+=e.clientX-lastX;oy+=e.clientY-lastY}
  else{hoverNode=nodeAt(e.clientX,e.clientY);canvas.style.cursor=hoverNode?'pointer':'grab'}
  lastX=e.clientX;lastY=e.clientY;
});
addEventListener('mouseup',()=>{dragNode=null;panning=false});
canvas.addEventListener('click',e=>{const n=nodeAt(e.clientX,e.clientY);if(n)openPanel(n)});
canvas.addEventListener('wheel',e=>{
  e.preventDefault();
  const f=e.deltaY<0?1.12:0.89;const ns=Math.min(4,Math.max(0.1,scale*f));
  const[wx,wy]=toWorld(e.clientX,e.clientY);
  ox-=wx*(ns-scale);oy-=wy*(ns-scale);scale=ns;
},{passive:false});
searchEl.addEventListener('input',()=>{query=searchEl.value.trim().toLowerCase()});

function openPanel(n){
  document.getElementById('p-title').textContent=n.label;
  document.getElementById('p-meta').textContent=n.kind+(n.file?' · '+n.file+(n.line>1?':'+n.line:''):'')+' · community '+n.community+' · degree '+n.degree;
  const ul=document.getElementById('p-edges');ul.innerHTML='';
  links.filter(l=>l.s===n||l.t===n).slice(0,80).forEach(l=>{
    const other=l.s===n?l.t:l.s,arrow=l.s===n?'→':'←';
    const li=document.createElement('li');
    li.innerHTML=arrow+' <b></b> <span class="tag">['+l.rel+'] ['+l.origin+']</span>';
    li.querySelector('b').textContent=other.label;
    ul.appendChild(li);
  });
  panel.classList.add('open');
}
})();
</script>
</body>
</html>
`
