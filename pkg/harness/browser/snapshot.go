package browser

import (
	"fmt"
	"strings"
)

const (
	defaultMaxSnapshotElements = 200
	defaultMaxSnapshotDOMNodes = 300
)

// BuildSnapshotScript constructs the self-contained page IIFE that builds dual-track
// semantic DOM + actionable elements with stable e1..eN handles stored in window.__nexRefs.
// Supports traversing Open Shadow DOM and same-origin iframes with viewport offset adjustment.
func BuildSnapshotScript(scopeSelector string, maxElements int, includeHidden bool) string {
	if maxElements <= 0 {
		maxElements = defaultMaxSnapshotElements
	}
	selLit := `""`
	if strings.TrimSpace(scopeSelector) != "" {
		selLit = quoteJS(scopeSelector)
	}
	hiddenFlag := "false"
	if includeHidden {
		hiddenFlag = "true"
	}

	return fmt.Sprintf(`(function(){
var MAX=%d;
var DOM_MAX=%d;
var INCLUDE_HIDDEN=%s;
var SCOPE_SEL=%s;
var ACTION_SEL='a[href], button, input, textarea, select, [role], [onclick], [tabindex], summary, label, [contenteditable]';
var DOM_SEL='body, main, nav, header, footer, aside, section, article, h1, h2, h3, h4, h5, h6, p, ul, ol, li, dl, dt, dd, blockquote, pre, code, table, caption, thead, tbody, tfoot, tr, th, td, form, fieldset, legend, figure, figcaption, img, canvas, svg, a[href], button, input, textarea, select, option, summary, label, [role], [aria-label], [contenteditable]';
function safeId(id){return /^[A-Za-z][A-Za-z0-9_-]*$/.test(id);}
function isHidden(el){
  try{
    var st=el.ownerDocument.defaultView.getComputedStyle(el);
    if(!st)return false;
    if(st.display==='none'||st.visibility==='hidden'||st.opacity==='0')return true;
    var r=el.getBoundingClientRect();
    if(r.width<=0&&r.height<=0)return true;
    return false;
  }catch(e){return false;}
}
function accName(el){
  var n=el.getAttribute('aria-label')||el.getAttribute('alt')||el.getAttribute('title')||el.getAttribute('placeholder')||'';
  if(!n){n=(el.innerText||el.textContent||'');}
  return String(n||'').trim().replace(/\s+/g,' ').slice(0,120);
}
function semanticName(el,tag){
  var n=el.getAttribute('aria-label')||el.getAttribute('alt')||el.getAttribute('title')||el.getAttribute('placeholder')||'';
  if(!n&&/^(a|button|input|textarea|select|summary)$/.test(tag))n=accName(el);
  return String(n||'').trim().replace(/\s+/g,' ').slice(0,120);
}
function attrsOf(el){
  var out={};
  var keys=['id','href','name','type','placeholder','title','alt','role','aria-label','data-testid','data-test','data-qa'];
  for(var i=0;i<keys.length;i++){
    var v=el.getAttribute(keys[i]);
    if(v!=null&&String(v).trim()!=='')out[keys[i]]=String(v).trim().slice(0,200);
  }
  return out;
}
function depthOf(el){
  var doc=el.ownerDocument;
  if(el===doc.body)return 0;
  var d=0;var p=el.parentElement;
  while(p&&p!==doc.body&&d<24){d++;p=p.parentElement;}
  return d;
}
function semanticText(el,tag){
  if(!/^(h[1-6]|p|li|dt|dd|blockquote|pre|code|caption|th|td|label|summary|button|a|option|legend|figcaption)$/.test(tag))return '';
  return String(el.innerText||el.textContent||'').trim().replace(/\s+/g,' ').slice(0,260);
}
function implicitRole(el,tag){
  if(tag==='a'&&el.getAttribute('href')!=null)return 'link';
  if(tag==='button')return 'button';
  if(tag==='select')return 'combobox';
  if(tag==='textarea')return 'textbox';
  if(tag==='summary')return 'button';
  if(tag==='input'){
    var ty=(el.getAttribute('type')||'text').toLowerCase();
    if(ty==='checkbox')return 'checkbox';
    if(ty==='radio')return 'radio';
    if(ty==='button'||ty==='submit'||ty==='reset')return 'button';
    if(ty==='search')return 'searchbox';
    return 'textbox';
  }
  return '';
}
function buildSelector(el){
  if(el.id&&safeId(el.id))return '#'+el.id;
  var parts=[];var cur=el;var depth=0;
  while(cur&&cur.nodeType===1&&depth<6){
    if(cur.id&&safeId(cur.id)){parts.unshift('#'+cur.id);break;}
    var t=cur.tagName.toLowerCase();
    var idx=1;var sib=cur.previousElementSibling;
    while(sib){if(sib.tagName===cur.tagName)idx++;sib=sib.previousElementSibling;}
    parts.unshift(t+':nth-of-type('+idx+')');
    cur=cur.parentElement;depth++;
  }
  return parts.join(' > ');
}
function collectInRoot(root,selector,outList){
  if(!root||typeof root.querySelectorAll!=='function')return;
  var found=root.querySelectorAll(selector);
  for(var i=0;i<found.length;i++){
    outList.push(found[i]);
  }
  var all=root.querySelectorAll('*');
  for(var j=0;j<all.length;j++){
    if(all[j].shadowRoot){
      collectInRoot(all[j].shadowRoot,selector,outList);
    }
  }
}
function collectFrames(rootNode,selector,framePath,offX,offY,outEntries){
  var raw=[];
  collectInRoot(rootNode,selector,raw);
  for(var i=0;i<raw.length;i++){
    outEntries.push({el:raw[i],framePath:framePath,offX:offX,offY:offY});
  }
  var iframes=(rootNode&&rootNode.querySelectorAll)?rootNode.querySelectorAll('iframe'):[];
  for(var f=0;f<iframes.length;f++){
    try{
      var ifr=iframes[f];
      if(!INCLUDE_HIDDEN&&isHidden(ifr))continue;
      var cdoc=ifr.contentDocument;
      if(!cdoc)continue;
      var fr=ifr.getBoundingClientRect();
      var nextPath=framePath?(framePath+'>'+f):String(f);
      collectFrames(cdoc,selector,nextPath,offX+fr.left,offY+fr.top,outEntries);
    }catch(e){}
  }
}
try{window.__nexRefs=new Map();}catch(e){window.__nexRefs=null;}
var elRef=(typeof WeakMap!=='undefined')?new WeakMap():null;
var vw=window.innerWidth||document.documentElement.clientWidth||0;
var vh=window.innerHeight||document.documentElement.clientHeight||0;
var root=document;
if(SCOPE_SEL){
  var scoped=document.querySelector(SCOPE_SEL);
  if(scoped)root=scoped;
}
var actionEntries=[];
collectFrames(root,ACTION_SEL,'',0,0,actionEntries);
var elements=[];var truncated=false;var count=0;
for(var i=0;i<actionEntries.length;i++){
  var entry=actionEntries[i];var el=entry.el;
  if(!INCLUDE_HIDDEN&&isHidden(el))continue;
  if(count>=MAX){truncated=true;break;}
  count++;
  var ref='e'+count;
  if(window.__nexRefs)window.__nexRefs.set(ref,el);
  if(elRef)elRef.set(el,ref);
  var r=el.getBoundingClientRect();
  var ax=Math.round(r.left+entry.offX);
  var ay=Math.round(r.top+entry.offY);
  var rect={x:ax,y:ay,width:Math.round(r.width),height:Math.round(r.height)};
  var inViewport=(ay+r.height)>0&&ay<vh&&(ax+r.width)>0&&ax<vw;
  var tag=el.tagName.toLowerCase();
  var out={ref:ref,tag:tag,selector:buildSelector(el),rect:rect,in_viewport:inViewport};
  if(entry.framePath)out.frame_path=entry.framePath;
  if(elRef){
    var p=el.parentElement;
    while(p){var pr=elRef.get(p);if(pr){out.parent_ref=pr;break;}p=p.parentElement;}
  }
  var role=el.getAttribute('role')||implicitRole(el,tag);
  if(role)out.role=role;
  var name=accName(el);
  if(name)out.name=name;
  var text=(el.innerText||'').trim().replace(/\s+/g,' ').slice(0,100);
  if(text)out.text=text;
  var attrs=attrsOf(el);
  if(Object.keys(attrs).length)out.attributes=attrs;
  if((tag==='input'||tag==='textarea'||tag==='select')&&el.value!=null&&el.value!==''){
    if((el.getAttribute('type')||'').toLowerCase()==='password'){
      out.value='[REDACTED]';
    }else{
      out.value=String(el.value).slice(0,120);
    }
  }
  if(el.disabled===true)out.disabled=true;
  if(tag==='input'&&(el.type==='checkbox'||el.type==='radio'))out.checked=el.checked===true;
  elements.push(out);
}
var domEntries=[];
collectFrames(root,DOM_SEL,'',0,0,domEntries);
var dom=[];var domTruncated=false;
for(var di=0;di<domEntries.length;di++){
  var deEntry=domEntries[di];var de=deEntry.el;
  if(!INCLUDE_HIDDEN&&isHidden(de))continue;
  if(dom.length>=DOM_MAX){domTruncated=true;break;}
  var dr=de.getBoundingClientRect();
  var dy=Math.round(dr.top+deEntry.offY);
  var dx=Math.round(dr.left+deEntry.offX);
  var dtag=de.tagName.toLowerCase();
  var dn={tag:dtag,depth:depthOf(de),in_viewport:(dy+dr.height)>0&&dy<vh&&(dx+dr.width)>0&&dx<vw};
  if(elRef){var dref=elRef.get(de);if(dref)dn.ref=dref;}
  var drole=de.getAttribute('role')||implicitRole(de,dtag);
  if(drole)dn.role=drole;
  var dname=semanticName(de,dtag);
  if(dname)dn.name=dname;
  var dtext=semanticText(de,dtag);
  if(dtext)dn.text=dtext;
  var dattrs=attrsOf(de);
  if(Object.keys(dattrs).length)dn.attributes=dattrs;
  dom.push(dn);
}
return {url:location.href,title:document.title,viewport:{width:vw,height:vh},dom:dom,dom_truncated:domTruncated,elements:elements,truncated:truncated};
})()`, maxElements, defaultMaxSnapshotDOMNodes, hiddenFlag, selLit)
}

func quoteJS(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			sb.WriteString(`\\`)
		case '"':
			sb.WriteString(`\"`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}
