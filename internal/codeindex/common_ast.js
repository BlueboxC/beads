// Parse supplied text with fixed embedded grammars. No project host or execution.
const fs=require('fs'),vm=require('vm');
const input=JSON.parse(fs.readFileSync(0,'utf8'));
const runtime={module:{exports:{}},exports:{},Uint8Array,TextDecoder,TextEncoder,URL,WebAssembly,
 document:{currentScript:{src:'https://beads.invalid/tree-sitter.js'}},console:{log(){},warn(){},error(){}}};
vm.runInNewContext(input.runtime,runtime,{timeout:10000});
const {Parser,Language}=runtime.module.exports;
const field=(n,k)=>n?.childForFieldName(k),child=(n,t)=>n?.namedChildren.find(c=>c.type===t);
const ident=n=>n&&['name','identifier','type_identifier','simple_identifier','field_identifier','word','function_name','simple_name','tag_name','attribute_name','property_name','class_name','id_name','bare_key'].includes(n.type)?n.text:'';
const safe=s=>s&&s.length<=128&&!/[\x00-\x1f\x7f]/.test(s)?s:'';
function literal(n){
 if(!n)return '';
 if(['string','string_literal','string_value','quoted_attribute_value'].includes(n.type)){
  if(n.namedChildren.some(c=>!['string_content','attribute_value'].includes(c.type)))return '';
  return n.text.slice(1,-1);
 }
 return ['word','attribute_value','command_name'].includes(n.type)?n.text:'';
}
const localPath=s=>s&&s.length<=256&&!/[\s\x00-\x1f\x7f$`\\:*?#{&]/.test(s)&&!s.startsWith('/')?s:'';
function analyze(source,parser){
 const out={path:source.path,symbols:[],imports:[],calls:[],blocked:{}};
 const tree=parser.parse(source.content);if(!tree)throw Error('parse failed');
 try{
  if(tree.rootNode.hasError){out.parse_error='SyntaxError';return out}
  let scope='',steps=0;const kinds=new Map();
  const owner=()=>source.path+'::'+scope,line=n=>n.startPosition.row+1;
  function definition(n,name,kind,walk){
   name=safe(name);if(!name)return;
   const parent=owner(),q=scope?scope+'.'+name:name;
   out.symbols.push({id:source.path+'::'+q,name:q,kind,line:line(n),end_line:n.endPosition.row+1,parent,_start:n.startIndex,_end:n.endIndex});
   kinds.set(q,kind);const prev=scope;scope=q;walk();scope=prev;
  }
  function reference(n,name,member='path',uncertain=false){
   if(name)out.imports.push({owner:owner(),name,alias:'',line:line(n),level:0,member,uncertain,_offset:n.startIndex});
  }
  function call(n,name){out.calls.push({owner:owner(),name:safe(name)||'<dynamic>',line:line(n),_offset:n.startIndex})}
  function block(name){if(name)(out.blocked[owner()]??=new Set()).add(name)}
  function declarator(n){let d=field(n,'declarator');for(let i=0;d&&i<16;i++){if(ident(d))return ident(d);d=field(d,'declarator')||d.namedChildren[0]}return ''}
  function key(n){
   if(!n)return '';if(input.language==='json')try{return safe(JSON.parse(n.text))}catch{return ''}
   if(['flow_node','plain_scalar'].includes(n.type)&&n.namedChildCount===1)return key(n.namedChildren[0]);
   if(n.type==='string_scalar')return safe(n.text);
   if(['double_quote_scalar','single_quote_scalar','quoted_key'].includes(n.type))return safe(n.text.slice(1,-1));
   if(n.type==='dotted_key')return n.namedChildren.map(key).filter(Boolean).join('.');
   return safe(ident(n));
  }
  function visit(n,depth=0){
   if(++steps>200000||depth>256)throw RangeError('AST bound');
   const t=n.type,walk=()=>{for(const c of n.namedChildren)visit(c,depth+1)},lang=input.language;
   if(t.includes('comment')||['anonymous_function','anonymous_function_creation_expression','lambda_literal','lambda_expression','closure_expression','raw_text','text','string_literal','string','heredoc_body','heredoc','nowdoc'].includes(t))return;
   if(lang==='json'||lang==='yaml'||lang==='toml'){
    if(['pair','block_mapping_pair','flow_pair'].includes(t)){
     const k=field(n,'key')||n.namedChildren[0],v=field(n,'value')||n.namedChildren[1];
     definition(n,key(k),'key',()=>{if(v)visit(v,depth+1)});return;
    }
    if(t==='table'||t==='table_array_element'){definition(n,key(n.namedChildren[0]),'table',()=>{for(const c of n.namedChildren.slice(1))visit(c,depth+1)});return}
    if(t==='array'||t==='block_sequence'||t==='flow_sequence'){
     let i=0;for(const c of n.namedChildren)definition(c,'['+(i++)+']','item',()=>visit(c,depth+1));return;
    }
    if(['document','stream','object','block_node','block_mapping','flow_node','flow_mapping','block_sequence_item','inline_table'].includes(t))walk();
    return; // Scalar values, tags and alias expansion are never stored/evaluated.
   }
   if(lang==='html'){
    if(['element','script_element','style_element'].includes(t)){
     const tag=child(n,'start_tag')||child(n,'self_closing_tag'),name=ident(child(tag,'tag_name'));
     definition(n,name,'element',()=>{
      for(const a of tag?.namedChildren||[]){if(a.type!=='attribute')continue;
       const label=ident(child(a,'attribute_name'));definition(a,label,'attribute',()=>{});
       if(['src','href'].includes(label))reference(a,localPath(literal(a.namedChildren[1])));
      }
      for(const c of n.namedChildren)if(['element','script_element','style_element'].includes(c.type))visit(c,depth+1);
     });return;
    }walk();return;
   }
   if(lang==='css'){
    if(t==='import_statement'){const s=child(n,'string_value')||child(child(n,'call_expression'),'arguments')?.namedChildren[0];reference(n,localPath(literal(s)));return}
    if(t==='rule_set'){definition(n,'rule@L'+line(n),'rule',walk);return}
    const selectors={class_selector:['class_name','.'],id_selector:['id_name','#'],pseudo_class_selector:['class_name',':'],pseudo_element_selector:['tag_name','::']};
    if(selectors[t]){const [type,prefix]=selectors[t];definition(n,prefix+ident(child(n,type)),'selector',()=>{for(const c of n.namedChildren)if(c.type==='tag_name'||c.type.endsWith('_selector'))visit(c,depth+1)});return}
    if(t==='tag_name'){definition(n,ident(n),'selector',()=>{});return}
    if(t==='declaration'){definition(n,ident(child(n,'property_name')),'property',()=>{});return}
    // Selector attribute values, declarations and string/url values are omitted.
    if(['attribute_selector','plain_value','string_value','call_expression'].includes(t))return;
    walk();return;
   }
   if(lang==='graphql'){
    const defs={object_type_definition:'type',interface_type_definition:'interface',enum_type_definition:'enum',union_type_definition:'union',scalar_type_definition:'scalar',input_object_type_definition:'input',operation_definition:'operation',fragment_definition:'fragment',field_definition:'field',input_value_definition:'field',enum_value_definition:'enum_value',directive_definition:'directive'};
    if(defs[t]){const name=ident(child(n,'name'))||ident(child(child(n,'fragment_name'),'name'))|| (t==='operation_definition'?'anonymous@L'+line(n):'');definition(n,name,defs[t],walk);return}
    if(t==='named_type'){reference(n,ident(child(n,'name')),'type',true);return}
    if(t==='fragment_spread'){reference(n,ident(child(child(n,'fragment_name'),'name')),'fragment',true);return}
    walk();return;
   }
   if(lang==='sql'){
    const objectName=n=>n?.namedChildren.filter(c=>c.type==='identifier').map(c=>safe(c.text)).filter(Boolean).join('.');
    if(['create_table','create_view','create_function','create_procedure','create_index'].includes(t)){definition(n,objectName(child(n,'object_reference'))||ident(child(n,'identifier')),t.slice(7),walk);return}
    if(t==='column_definition'){definition(n,ident(field(n,'name')),'column',()=>{});return}
    if(t==='relation'){reference(n,objectName(child(n,'object_reference')),'table',true);return}
    walk();return;
   }
   if(lang==='php'){
    if(t==='namespace_definition'){
     const name=field(n,'name')?.namedChildren.map(ident).filter(Boolean).join('.');
     if(field(n,'body'))definition(n,name,'namespace',walk);else{definition(n,name,'namespace',()=>{});scope=name||''}return;
    }
    if(t==='namespace_use_clause'){const target=n.namedChildren.find(c=>['qualified_name','name'].includes(c.type));reference(n,safe(target?.text),'namespace',true);return}
    if(['include_expression','include_once_expression','require_expression','require_once_expression'].includes(t)){reference(n,localPath(literal(n.namedChildren[0])));return}
    const types={class_declaration:'class',interface_declaration:'interface',trait_declaration:'trait',enum_declaration:'enum'};
    if(types[t]){definition(n,ident(field(n,'name')),types[t],walk);return}
    if(t==='function_definition'||t==='method_declaration'){definition(n,ident(field(n,'name')),t==='function_definition'?'function':'method',walk);return}
    if(t==='function_call_expression'){call(n,ident(field(n,'function')));walk();return}
    if(['member_call_expression','scoped_call_expression','nullsafe_member_call_expression','object_creation_expression'].includes(t)){call(n,'<receiver>');walk();return}
   }
   if(lang==='c'){
    if(t==='preproc_include'){const p=field(n,'path');reference(n,localPath(literal(p))||'<computed-or-system-include>','path',p?.type!=='string_literal');return}
    if(t.startsWith('preproc_')){block('*');walk();return}
    if(t==='function_definition'){definition(n,declarator(n),'function',walk);return}
    if(['struct_specifier','union_specifier','enum_specifier'].includes(t)){if(field(n,'body'))definition(n,ident(field(n,'name')),t.split('_')[0],walk);return}
    if(t==='type_definition'){definition(n,declarator(n),'type',()=>{});return}
    if(['parameter_declaration','init_declarator','declaration'].includes(t))block(declarator(n));
    if(t==='call_expression'){call(n,ident(field(n,'function')));walk();return}
   }
   if(lang==='bash'){
    if(t==='function_definition'){definition(n,ident(field(n,'name')),'function',walk);return}
    if(t==='command'){
     const name=ident(child(field(n,'name'),'word'));
     if(name==='source'||name==='.'){reference(n,localPath(literal(field(n,'argument'))),'path');return}
     call(n,/^[A-Za-z_][\w-]*$/.test(name)?name:'<dynamic>');walk();return;
    }
   }
   if(lang==='powershell'){
    if(t==='function_statement'){definition(n,ident(child(n,'function_name')),'function',walk);return}
    if(t==='class_statement'){definition(n,ident(child(n,'simple_name')),'class',walk);return}
    if(t==='command'){
     const p=field(n,'command_name'),name=p?.type==='command_name'?p.text:'',op=child(n,'command_invokation_operator');
     if(op?.text==='.'){reference(n,localPath(literal(child(p,'command_name'))),'path');return}
     call(n,/^[A-Za-z_][\w-]*$/.test(name)?name:'<dynamic>');walk();return;
    }
   }
   if(lang==='kotlin'||lang==='swift'){
    if(t==='package_header'){out.package=child(n,'identifier')?.text||'';return}
    if(t==='import_header'||t==='import_declaration'){reference(n,safe(child(n,'identifier')?.text),'namespace',true);return}
    const types={class_declaration:'class',object_declaration:'object',protocol_declaration:'interface',type_alias:'type',typealias_declaration:'type'};
    if(types[t]){const kind=n.children.find(c=>['struct','enum','actor','extension','interface'].includes(c.type))?.type||types[t];definition(n,ident(child(n,'type_identifier')),kind,walk);return}
    if(t==='function_declaration'){definition(n,ident(child(n,'simple_identifier')),['class','object','interface','struct','enum','actor','extension'].includes(kinds.get(scope))?'method':'function',walk);return}
    if(t==='property_declaration'){definition(n,ident(child(n,'simple_identifier')||child(child(n,'variable_declaration'),'simple_identifier')),'property',()=>{});return}
    if(t==='call_expression'){call(n,ident(n.namedChildren[0]));walk();return}
   }
   if(lang==='dart'){
    if(t==='import_specification'||t==='export_specification'){reference(n,localPath(literal(child(child(n,'configurable_uri'),'uri')?.namedChildren[0])),'path',n.namedChildren.some(c=>c.type==='configuration_uri'));return}
    if(['class_definition','mixin_declaration','enum_declaration','extension_declaration','type_alias'].includes(t)){definition(n,ident(child(n,'identifier')),t==='class_definition'?'class':t.replace('_declaration',''),walk);return}
    if(t==='method_signature'||t==='function_signature'){
     const sig=t==='method_signature'?child(n,'function_signature'):n,name=ident(child(sig,'identifier')),body=n.nextNamedSibling;
     definition(n,name,t==='method_signature'?'method':'function',()=>{if(body?.type==='function_body')for(const c of body.namedChildren)visit(c,depth+1)});return;
    }
    if(t==='function_body'&&n.previousNamedSibling&&['function_signature','method_signature'].includes(n.previousNamedSibling.type))return;
    if(t==='selector'&&child(n,'argument_part'))call(n,ident(n.previousNamedSibling));
   }
   walk();
  }
  visit(tree.rootNode);
  // Keep repeated elements, duplicate keys and overloads distinct; rebind children
  // only when their source span belongs to this exact parent occurrence.
  const counts=new Map();for(const s of out.symbols)counts.set(s.id,(counts.get(s.id)||0)+1);
  for(let i=0;i<out.symbols.length;i++){
   const s=out.symbols[i];if(counts.get(s.id)<=1)continue;const old=s.id;s.id+='@L'+s.line+'_'+i;
   if(out.blocked[old])out.blocked[s.id]=out.blocked[old];
   for(const c of out.calls)if(c.owner===old&&c._offset>=s._start&&c._offset<s._end)c.owner=s.id;
   for(const c of out.imports)if(c.owner===old&&c._offset>=s._start&&c._offset<s._end)c.owner=s.id;
   for(const c of out.symbols)if(c.parent===old&&c._start>=s._start&&c._start<s._end)c.parent=s.id;
  }
  for(const s of out.symbols){delete s._start;delete s._end}for(const c of [...out.calls,...out.imports])delete c._offset;
  out.blocked=Object.fromEntries(Object.entries(out.blocked).filter(([,v])=>v).map(([k,v])=>[k,[...v].sort()]));return out;
 }finally{tree.delete()}
}
(async()=>{
 await Parser.init({wasmBinary:new Uint8Array(Buffer.from(input.wasm,'base64')),wasmMemory:new WebAssembly.Memory({initial:512,maximum:8192})});
 const grammar=await Language.load(new Uint8Array(Buffer.from(input.grammar,'base64'))),parser=new Parser();parser.setLanguage(grammar);
 try{
  const files=input.files.map(s=>{try{return analyze(s,parser)}catch(e){return{path:s.path,symbols:[],imports:[],calls:[],blocked:{},parse_error:e instanceof RangeError?'ASTLimit':'ASTError'}}});
  process.stdout.write(JSON.stringify({node:process.versions.node,files}));
 }finally{parser.delete()}
})().catch(()=>{process.stderr.write('AST runtime failed');process.exitCode=1});
