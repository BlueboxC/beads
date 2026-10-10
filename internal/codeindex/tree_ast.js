// Supplied UTF-8 only. The pinned WASM runtime has no filesystem/network host.
const fs=require('fs'),vm=require('vm');
const input=JSON.parse(fs.readFileSync(0,'utf8'));
const runtime={module:{exports:{}},exports:{},Uint8Array,TextDecoder,TextEncoder,URL,WebAssembly,
 document:{currentScript:{src:'https://beads.invalid/tree-sitter.js'}},
 console:{log(){},warn(){},error(){}}};
vm.runInNewContext(input.runtime,runtime,{timeout:10000});
const {Parser,Language}=runtime.module.exports;
const field=(n,k)=>n?.childForFieldName(k);
const child=(n,t)=>n?.namedChildren.find(c=>c.type===t);
function identifier(n){
 if(!n)return '';
 if(['identifier','type_identifier','field_identifier','namespace_identifier','shorthand_field_identifier','self','crate','super','this','base'].includes(n.type))return n.text;
 if(['scoped_identifier','scoped_type_identifier','qualified_identifier','qualified_name','alias_qualified_name'].includes(n.type)){
  const a=identifier(field(n,'scope')||field(n,'path')||field(n,'qualifier')||n.namedChildren[0]);
  const b=identifier(field(n,'name')||n.namedChildren.at(-1));return a&&b?a+'.'+b:'';
 }
 if(n.type==='nested_namespace_specifier')return n.namedChildren.map(identifier).filter(Boolean).join('.');
 if(n.type==='destructor_name')return '~'+identifier(n.namedChildren[0]);
 if(n.type==='operator_name')return n.text;
 if(n.type==='member_access_expression'){const a=identifier(field(n,'expression')),b=identifier(field(n,'name'));return a&&b?a+'.'+b:''}
 if(n.type==='generic_type')return identifier(field(n,'type'));
 if(['generic_name','template_function','template_method','template_type'].includes(n.type))return identifier(field(n,'name')||n.namedChildren[0]);
 return '';
}
function analyze(source,parser){
 const out={path:source.path,symbols:[],imports:[],calls:[],blocked:{}};
 const tree=parser.parse(source.content);if(!tree)throw Error('parse failed');
 try{
  if(tree.rootNode.hasError){out.parse_error='SyntaxError';return out}
  let scope='',steps=0;const kinds=new Map();
  const owner=()=>source.path+'::'+scope;
  const line=n=>n.startPosition.row+1;
  function block(name){if(!name)return;const key=owner();(out.blocked[key]??=new Set()).add(name)}
  function pattern(n){if(!n)return;if(['identifier','field_identifier','shorthand_field_identifier','self'].includes(n.type)){block(identifier(n));return}for(const c of n.namedChildren)pattern(c)}
  function addImport(n,name,alias='',extra={}){if(name)out.imports.push({owner:owner(),name,alias,line:line(n),level:0,...extra})}
  function definition(n,name,kind,body,stat=false){
   if(!name){return}
   const parent=owner(),q=scope?scope+'.'+name:name;
   out.symbols.push({id:source.path+'::'+q,name:q,kind,line:line(n),end_line:n.endPosition.row+1,parent,static:stat,uncertain:input.language==='rust'&&n.previousNamedSibling?.type==='attribute_item'||false,_start:n.startIndex,_end:n.endIndex});kinds.set(q,kind);
   const prev=scope;scope=q;body();scope=prev;
  }
  function modifiers(n){return n.namedChildren.some(c=>(c.type==='modifier'||c.type==='storage_class_specifier')&&c.text==='static')||child(n,'modifiers')?.children.some(c=>c.type==='static')||false}
  function functionDeclarator(n){let d=field(n,'declarator');for(let i=0;d&&i<16;i++){if(d.type==='function_declarator')return d;d=field(d,'declarator')}return null}
  function declarationName(n){let d=field(n,'declarator');for(let i=0;d&&i<16;i++){const name=identifier(d);if(name)return name;d=field(d,'declarator')||(d.type==='parenthesized_declarator'?d.namedChildren[0]:null)}return ''}
  function visit(n,depth=0){
   if(++steps>200000||depth>256)throw RangeError('AST bound');
   const t=n.type,walk=()=>{for(const c of n.namedChildren)visit(c,depth+1)};
   if(['comment','line_comment','block_comment','lambda_expression','closure_expression','anonymous_method_expression','lambda','macro_definition','token_tree','attribute_item'].includes(t))return;
   if(input.language==='java'){
    if(t==='package_declaration'){out.package=identifier(n.namedChildren.find(c=>['identifier','scoped_identifier'].includes(c.type)));return}
    if(t==='import_declaration'){
     const target=identifier(n.namedChildren.find(c=>['identifier','scoped_identifier'].includes(c.type))),star=!!child(n,'asterisk'),stat=n.children.some(c=>c.type==='static');
     addImport(n,target,target.split('.').at(-1),{type_only:!stat,uncertain:star,member:stat?target.split('.').at(-1):''});if(star)block('*');return;
    }
    if(t==='method_invocation'){
     const name=identifier(field(n,'name')),object=field(n,'object'),prefix=identifier(object);
     out.calls.push({owner:owner(),name:object?(prefix?prefix+'.'+name:'<dynamic>'):name||'<dynamic>',line:line(n),_offset:n.startIndex});walk();return;
    }
   }
   if(t==='object_creation_expression'){out.calls.push({owner:owner(),name:'<constructor>',line:line(n),_offset:n.startIndex});for(const c of n.namedChildren)if(c.type!=='class_body')visit(c,depth+1);return}
   if(t==='macro_invocation'){out.calls.push({owner:owner(),name:'<macro>',line:line(n),_offset:n.startIndex});return}
   if(t==='using_directive'){
    const names=n.namedChildren.filter(c=>c.type!=='modifier'),alias=identifier(field(n,'name')),target=identifier(names.at(-1));
    addImport(n,target,alias,{type_only:true,uncertain:true});if(alias)block(alias);return;
   }
   if(t==='use_declaration'){
    const a=field(n,'argument'),alias=identifier(field(a,'alias')),target=identifier(field(a,'path')||a);
    addImport(n,target||'<use-group>',alias||target.split('.').at(-1),{uncertain:true});if(target)block(alias||target.split('.').at(-1));else block('*');return;
   }
   if(t==='extern_crate_declaration'){addImport(n,identifier(field(n,'name')),identifier(field(n,'alias')),{uncertain:true});block('*');return}
   if(t==='preproc_include'){
    const p=field(n,'path'),value=p?.type==='string_literal'?child(p,'string_content')?.text:p?.type==='system_lib_string'?p.text.slice(1,-1):'<computed-include>';
    addImport(n,value||'<computed-include>','',{uncertain:p?.type!=='string_literal'});return;
   }
   if(['preproc_def','preproc_function_def'].includes(t)){block(identifier(field(n,'name')));return}
   if(t.startsWith('preproc_')){block('*');walk();return} // Both branches are syntax, not an evaluated build.
   if(t==='using_declaration'||t==='namespace_alias_definition'){block('*');return}
   if(t==='file_scoped_namespace_declaration'){
    const name=identifier(field(n,'name'));definition(n,name,'namespace',()=>{});scope=name;return;
   }
   if(['namespace_declaration','namespace_definition'].includes(t)){
    const name=identifier(field(n,'name'));if(name)definition(n,name,'namespace',walk);return;
   }
   if(t==='mod_item'){
    const name=identifier(field(n,'name'));if(!field(n,'body'))addImport(n,name,name,{uncertain:n.previousNamedSibling?.type==='attribute_item'});definition(n,name,'module',walk);return;
   }
   if(t==='impl_item'){
    const name=identifier(field(n,'type'));if(!name)return;const prev=scope;scope=scope?scope+'.'+name:name;
    if(field(n,'trait'))block('*');walk();scope=prev;return;
   }
   const types={class_declaration:'class',interface_declaration:'interface',enum_declaration:'enum',record_declaration:'record',struct_declaration:'struct',record_struct_declaration:'struct',annotation_type_declaration:'interface',class_specifier:'class',struct_specifier:'struct',union_specifier:'struct',enum_specifier:'enum',struct_item:'struct',enum_item:'enum',trait_item:'interface',type_item:'type',type_alias_declaration:'type',alias_declaration:'type'};
   if(types[t]){definition(n,identifier(field(n,'name')),types[t],walk);return}
   if(['method_declaration','constructor_declaration','compact_constructor_declaration','local_function_statement','function_item','function_signature_item'].includes(t)){
    const name=identifier(field(n,'name')),type=kinds.get(scope),inType=['class','struct','interface','record','enum'].includes(type)||t==='function_item'&&n.parent?.parent?.type==='impl_item';
    const method=inType&&t!=='local_function_statement',kind=t.includes('constructor')?'constructor':t==='function_signature_item'?'method_declaration':method?'method':'function';
    const stat=input.language==='rust'?method&&!field(n.parent.parent,'trait')&&!field(n,'parameters')?.namedChildren.some(c=>c.type==='self_parameter'):modifiers(n);
    definition(n,name,kind,walk,stat);return;
   }
   if(t==='function_definition'){
    const d=functionDeclarator(n);if(d){const label=identifier(field(d,'declarator')),qualified=label.includes('.'),knownNamespace=kinds.get(label.slice(0,label.lastIndexOf('.')))==='namespace';const kind=['class','struct'].includes(kinds.get(scope))?'method':qualified&&!knownNamespace?'method_declaration':'function';definition(n,identifier(field(d,'declarator')),kind,walk,modifiers(n));return}
   }
   if((t==='declaration'||t==='field_declaration')&&input.language==='cpp'){
    const d=functionDeclarator(n);if(d){const name=identifier(field(d,'declarator'));if(name){definition(n,name,t==='field_declaration'?'method_declaration':'function_declaration',()=>{},modifiers(n));return}}
    block(declarationName(n));
   }
   if(['formal_parameter','spread_parameter','parameter','variable_declarator','catch_declaration','catch_formal_parameter','foreach_statement','enhanced_for_statement'].includes(t))pattern(field(n,'name')||field(n,'pattern'));
   if(['type_parameter','type_parameter_declaration'].includes(t))block(identifier(field(n,'name')||n.namedChildren[0]));
   if(t==='structured_binding_declarator')pattern(n);
   if(t==='self_parameter')block('self');
   if(t==='let_declaration'||t==='for_expression'||t==='let_condition'||t==='match_arm')pattern(field(n,'pattern'));
   if(t==='parameter_declaration'||t==='optional_parameter_declaration'||t==='init_declarator')block(declarationName(n));
   if(['call_expression','invocation_expression'].includes(t)){
    let f=field(n,'function');if(input.language==='cpp'&&f?.type==='primitive_type')block('*'); // Cast/declarator ambiguity needs type analysis.
    if(f?.type==='generic_function')f=field(f,'function');
    // Member/receiver expressions require type analysis; retain an unresolved call.
    out.calls.push({owner:owner(),name:identifier(f)||'<dynamic>',line:line(n),_offset:n.startIndex});
   }
   if(['assignment_expression','compound_assignment_expr'].includes(t))block(identifier(field(n,'left'))||'*');
   walk();
  }
  visit(tree.rootNode);
  // Overloads remain distinct graph nodes and never become a guessed single target.
  const counts=new Map();for(const s of out.symbols)counts.set(s.id,(counts.get(s.id)||0)+1);
  for(let i=0;i<out.symbols.length;i++){
   const s=out.symbols[i];if(counts.get(s.id)<=1)continue;const old=s.id;s.id+='@L'+s.line+'_'+i;
   out.blocked[s.id]=out.blocked[old];
   for(const c of out.calls)if(c.owner===old&&c._offset>=s._start&&c._offset<s._end)c.owner=s.id;
   for(const c of out.symbols)if(c.parent===old&&c._start>=s._start&&c._start<s._end)c.parent=s.id;
  }
  for(const s of out.symbols){delete s._start;delete s._end}for(const c of out.calls)delete c._offset;
  out.blocked=Object.fromEntries(Object.entries(out.blocked).filter(([,v])=>v).map(([k,v])=>[k,[...v].sort()]));
  return out;
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
