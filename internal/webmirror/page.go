package webmirror

// mirrorPage is the whole client: a self-contained, mobile-first chat view
// (dark theme, chat bubbles, tool chips, fixed input bar) in the spirit of
// opencode-manager's mobile UI, with zero external assets so it renders on
// any device that can reach the laptop. The token rides in the URL and is
// reused for the SSE stream, prompt/stop POSTs, completion lookups, and
// uploads. On top of the mirror it provides:
//   - notices: banner + beep + vibration + (when granted) system notification
//     when a run finishes or the session needs input;
//   - autocomplete: "/" surfaces the slash-command list (from /meta), "@"
//     surfaces workspace file completions (from /files);
//   - upload: the + button ships a phone file into .atcode/uploads/ and drops
//     its @path into the input box.
const mirrorPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<title>@code</title>
<style>
  :root{--bg:#0b0e14;--panel:#131722;--line:#232a3b;--text:#dbe2f0;--faint:#7a8399;
        --accent:#a6e22e;--user:#1d2a45;--err:#e5484d;--warn:#e5a839;}
  *{box-sizing:border-box;margin:0}
  html,body{height:100%}
  body{background:var(--bg);color:var(--text);
       font:15px/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
       display:flex;flex-direction:column}
  header{position:sticky;top:0;background:var(--panel);border-bottom:1px solid var(--line);
         padding:10px 14px;display:flex;align-items:center;gap:8px;z-index:2}
  header .dot{width:8px;height:8px;border-radius:50%;background:var(--faint)}
  header .dot.on{background:var(--accent)}
  header .title{font-weight:700}
  header .state{margin-left:auto;color:var(--faint);font-size:12px}
  #banner{display:none;position:sticky;top:41px;z-index:2;background:var(--warn);color:#0b0e14;
          font-weight:700;padding:8px 14px;font-size:13px}
  #banner.show{display:block}
  main{flex:1;overflow-y:auto;padding:12px 12px 96px;-webkit-overflow-scrolling:touch}
  .ev{margin:8px 0;max-width:94%;white-space:pre-wrap;word-break:break-word}
  .user{margin-left:auto;background:var(--user);border:1px solid var(--line);
        border-radius:12px 12px 4px 12px;padding:8px 12px}
  .assistant{background:var(--panel);border:1px solid var(--line);
             border-radius:12px 12px 12px 4px;padding:8px 12px}
  .assistant.live{border-color:var(--accent)}
  .thinking{color:var(--faint);font-size:13px;border-left:2px solid var(--line);padding:2px 10px}
  .tool{display:flex;gap:8px;align-items:baseline;color:var(--faint);font-size:13px;padding:2px 4px}
  .tool .name{color:var(--accent)}
  .tool.error .name{color:var(--err)}
  .system{color:var(--faint);font-size:13px;padding:2px 4px}
  .error{color:var(--err);font-size:13px;padding:2px 4px}
  .ask{background:var(--panel);border:1px solid var(--warn);border-radius:10px;
       padding:8px 12px;font-size:14px}
  #sug{display:none;position:fixed;left:0;right:0;bottom:58px;z-index:3;
       background:var(--panel);border-top:1px solid var(--line);max-height:40vh;overflow-y:auto}
  #sug.show{display:block}
  #sug .item{padding:10px 14px;border-bottom:1px solid var(--line)}
  #sug .item .k{color:var(--accent)}
  #sug .item .d{color:var(--faint);font-size:12px;display:block}
  form{position:fixed;left:0;right:0;bottom:0;display:flex;gap:8px;
       padding:10px 12px calc(10px + env(safe-area-inset-bottom));
       background:var(--panel);border-top:1px solid var(--line)}
  input[type=text]{flex:1;background:var(--bg);border:1px solid var(--line);border-radius:10px;
        color:var(--text);font:inherit;padding:10px 12px;outline:none;min-width:0}
  input[type=text]:focus{border-color:var(--accent)}
  button{background:var(--accent);color:#0b0e14;border:0;border-radius:10px;
         font:inherit;font-weight:700;padding:0 16px}
  button:disabled{opacity:.4}
  #plus{background:var(--panel);color:var(--text);border:1px solid var(--line);
        font-size:20px;padding:0 12px}
  #stop{background:var(--err);color:#fff;display:none}
  body.running #stop{display:block}
</style>
</head>
<body>
<header><span class="dot" id="dot"></span><span class="title">@code</span><span class="state" id="state">connecting…</span></header>
<div id="banner"></div>
<main id="log"></main>
<div id="sug"></div>
<form id="form">
  <button type="button" id="plus" title="Upload a file">+</button>
  <input type="file" id="file" hidden>
  <input type="text" id="box" placeholder="Message @code…" autocomplete="off" autocapitalize="off">
  <button type="button" id="stop">Stop</button>
  <button id="send">Send</button>
</form>
<script>
(function(){
  var token=new URLSearchParams(location.search).get('t')||'';
  function api(path){return path+(path.indexOf('?')<0?'?':'&')+'t='+encodeURIComponent(token)}
  var log=document.getElementById('log'),state=document.getElementById('state'),
      dot=document.getElementById('dot'),box=document.getElementById('box'),
      form=document.getElementById('form'),send=document.getElementById('send'),
      stop=document.getElementById('stop'),banner=document.getElementById('banner'),
      sug=document.getElementById('sug'),plus=document.getElementById('plus'),
      file=document.getElementById('file');
  var commands=[];
  fetch(api('/meta')).then(function(r){return r.json()})
    .then(function(m){commands=m.commands||[]}).catch(function(){});

  /* ---- notices: banner + beep + vibrate + system notification ---- */
  var lastSeq=-1,bannerTimer=null,baseTitle=document.title;
  function beep(){
    try{
      var ctx=new (window.AudioContext||window.webkitAudioContext)();
      var o=ctx.createOscillator(),g=ctx.createGain();
      o.connect(g);g.connect(ctx.destination);
      o.frequency.value=880;g.gain.value=.08;
      o.start();o.stop(ctx.currentTime+.18);
    }catch(_){}
  }
  function notice(n){
    banner.textContent=n.text;banner.className='show';
    clearTimeout(bannerTimer);
    bannerTimer=setTimeout(function(){banner.className=''},6000);
    document.title=(n.kind==='input'?'✋ ':'✅ ')+baseTitle;
    setTimeout(function(){document.title=baseTitle},8000);
    beep();
    try{navigator.vibrate&&navigator.vibrate(n.kind==='input'?[100,60,100]:120)}catch(_){}
    try{
      if(window.Notification&&Notification.permission==='granted'&&document.hidden){
        new Notification('@code',{body:n.text,tag:'sonut'});
      }
    }catch(_){}
  }

  /* ---- render ---- */
  function atBottom(){return log.scrollHeight-log.scrollTop-log.clientHeight<80}
  function render(snap){
    var stick=atBottom();
    log.innerHTML='';
    snap.events.forEach(function(e){
      var d=document.createElement('div');
      if(e.kind==='tool'){
        d.className='ev tool'+(e.status==='error'?' error':'');
        var n=document.createElement('span');n.className='name';
        n.textContent=(e.status==='running'?'⋯ ':'• ')+e.tool;
        var t=document.createElement('span');t.textContent=e.text||'';
        d.appendChild(n);d.appendChild(t);
      }else{
        d.className='ev '+e.kind+(e.live?' live':'');
        d.textContent=e.text;
      }
      log.appendChild(d);
    });
    state.textContent=snap.running?'working…':'idle';
    document.body.className=snap.running?'running':'';
    if(snap.notice&&snap.notice.seq){
      if(lastSeq>=0&&snap.notice.seq>lastSeq)notice(snap.notice);
      lastSeq=snap.notice.seq;
    }else if(lastSeq<0){lastSeq=0}
    if(stick)log.scrollTop=log.scrollHeight;
  }

  /* ---- SSE ---- */
  var es=null;
  function connect(){
    es=new EventSource(api('/events'));
    es.onopen=function(){dot.className='dot on'};
    es.onmessage=function(ev){try{render(JSON.parse(ev.data))}catch(_){}};
    es.onerror=function(){dot.className='dot';state.textContent='reconnecting…';
      es.close();setTimeout(connect,2000)};
  }
  connect();

  /* ---- autocomplete: "/" commands, "@" files ---- */
  var fileTimer=null;
  function hideSug(){sug.className='';sug.innerHTML=''}
  function showSug(items){
    sug.innerHTML='';
    if(!items.length){hideSug();return}
    items.slice(0,12).forEach(function(it){
      var d=document.createElement('div');d.className='item';
      var k=document.createElement('span');k.className='k';k.textContent=it.label;
      d.appendChild(k);
      if(it.desc){var s=document.createElement('span');s.className='d';s.textContent=it.desc;d.appendChild(s)}
      d.addEventListener('click',function(){it.apply();hideSug();box.focus()});
      sug.appendChild(d);
    });
    sug.className='show';
  }
  box.addEventListener('input',function(){
    var v=box.value;
    if(v.charAt(0)==='/'&&v.indexOf(' ')<0){
      var q=v.toLowerCase();
      showSug(commands.filter(function(c){return c.name.indexOf(q)===0}).map(function(c){
        return {label:c.usage||c.name,desc:c.description,
                apply:function(){box.value=c.name+' '}};
      }));
      return;
    }
    var m=v.match(/@([^\s@]*)$/);
    if(m){
      clearTimeout(fileTimer);
      fileTimer=setTimeout(function(){
        fetch(api('/files?q='+encodeURIComponent(m[1])))
          .then(function(r){return r.json()})
          .then(function(res){
            showSug((res.files||[]).map(function(p){
              return {label:'@'+p,desc:'',
                      apply:function(){box.value=v.slice(0,m.index)+'@'+p+' '}};
            }));
          }).catch(function(){});
      },150);
      return;
    }
    hideSug();
  });

  /* ---- upload ---- */
  plus.addEventListener('click',function(){file.click()});
  file.addEventListener('change',function(){
    if(!file.files.length)return;
    var fd=new FormData();
    fd.append('file',file.files[0]);
    plus.disabled=true;plus.textContent='…';
    fetch(api('/upload'),{method:'POST',body:fd})
      .then(function(r){return r.ok?r.json():Promise.reject()})
      .then(function(res){
        box.value=(box.value?box.value+' ':'')+'@'+res.path+' ';
        box.focus();
      })
      .catch(function(){banner.textContent='Upload failed';banner.className='show';
        setTimeout(function(){banner.className=''},4000)})
      .then(function(){plus.disabled=false;plus.textContent='+';file.value=''});
  });

  /* ---- stop / send ---- */
  stop.addEventListener('click',function(){
    stop.disabled=true;
    fetch(api('/stop'),{method:'POST'})
      .catch(function(){})
      .then(function(){stop.disabled=false});
  });
  form.addEventListener('submit',function(ev){
    ev.preventDefault();
    hideSug();
    try{if(window.Notification&&Notification.permission==='default')Notification.requestPermission()}catch(_){}
    var text=box.value.trim();
    if(!text)return;
    send.disabled=true;
    fetch(api('/prompt'),{method:'POST',
      headers:{'Content-Type':'application/json'},body:JSON.stringify({text:text})})
      .then(function(r){if(r.ok)box.value='';})
      .catch(function(){})
      .then(function(){send.disabled=false;box.focus()});
  });
})();
</script>
</body>
</html>`
