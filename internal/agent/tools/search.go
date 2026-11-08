package tools

import (
 "bytes"
 "context"
 "encoding/json"
 "fmt"
 "io/fs"
 "os"
 "os/exec"
 "path/filepath"
 "regexp"
 "sort"
 "strings"
 "time"
)

func globMatch(pattern, path string) bool {
 pattern=filepath.ToSlash(pattern);path=filepath.ToSlash(path)
 var re strings.Builder;re.WriteByte('^');for i:=0;i<len(pattern);i++ {switch pattern[i]{case '*':if i+1<len(pattern)&&pattern[i+1]=='*'{i++;if i+1<len(pattern)&&pattern[i+1]=='/'{i++;re.WriteString("(?:.*/)?")}else{re.WriteString(".*")}}else{re.WriteString("[^/]*")};case '?':re.WriteString("[^/]");default:re.WriteString(regexp.QuoteMeta(string(pattern[i])))}};re.WriteByte('$');ok,_:=regexp.MatchString(re.String(),path);if !ok&&!strings.Contains(pattern,"/"){ok,_=regexp.MatchString(re.String(),filepath.Base(path))};return ok
}
func candidates(ctx context.Context,env Env,root string)([]string,error){
 work,e:=resolve(env,".");if e!=nil{return nil,e};var files []string
 gitCtx,cancel:=context.WithTimeout(ctx,10*time.Second);defer cancel();cmd:=exec.CommandContext(gitCtx,"git","-C",work,"ls-files","--cached","--others","--exclude-standard","-z");out,e:=cmd.Output();if ctx.Err()!=nil{return nil,ctx.Err()};if gitCtx.Err()!=nil{return nil,gitCtx.Err()};relRoot,relErr:=filepath.Rel(work,root);if relErr!=nil||relRoot==".."||strings.HasPrefix(relRoot,".."+string(filepath.Separator)){e=fmt.Errorf("outside repository")}
 if e==nil {for _,name:=range strings.Split(string(out),"\x00"){if name==""{continue};p:=filepath.Join(work,name);rel,err:=filepath.Rel(root,p);if err!=nil||rel==".."||strings.HasPrefix(rel,".."+string(filepath.Separator)){continue};resolved,err:=resolve(env,p);if err!=nil{continue};info,err:=os.Stat(resolved);if err==nil&&info.Mode().IsRegular(){files=append(files,p)}}
 }else{e=filepath.WalkDir(root,func(p string,d fs.DirEntry,err error)error{if err!=nil{return err};if ctx.Err()!=nil{return ctx.Err()};if d.IsDir(){if p!=root&&(d.Name()==".git"||d.Name()=="node_modules"){return filepath.SkipDir};return nil};if _,err:=resolve(env,p);err!=nil{return nil};info,err:=os.Stat(p);if err==nil&&info.Mode().IsRegular(){files=append(files,p)};return nil});if e!=nil{return nil,e}}
 sort.Strings(files);return files,nil
}
func fileText(env Env,p string)(string,bool){safe,e:=resolve(env,p);if e!=nil{return "",false};b,e:=os.ReadFile(safe);if e!=nil{return "",false};prefix:=b;if len(prefix)>8192{prefix=prefix[:8192]};if bytes.IndexByte(prefix,0)>=0{return "",false};return string(b),true}
func findFiles(ctx context.Context,env Env,raw json.RawMessage)(Result,error){
 var a struct{Pattern,Path string;Limit int};if e:=parse(raw,&a);e!=nil{return failure(e)};if a.Pattern==""||a.Limit<0{return failure("invalid pattern or limit")};if a.Limit==0{a.Limit=1000};root,e:=resolve(env,a.Path);if e!=nil{return failure(e)};files,e:=candidates(ctx,env,root);if e!=nil{if ctx.Err()!=nil{return Result{},ctx.Err()};return failure(e)};var out strings.Builder;n:=0;for _,p:=range files{if e:=ctx.Err();e!=nil{return Result{},e};rel,e:=filepath.Rel(root,p);if e!=nil||!globMatch(a.Pattern,rel){continue};if _,ok:=fileText(env,p);!ok{continue};if n==a.Limit{out.WriteString("Output truncated.\n");break};fmt.Fprintln(&out,filepath.ToSlash(rel));n++};return Result{Text:truncate(out.String(),"Output truncated.")},nil
}
func grepFiles(ctx context.Context,env Env,raw json.RawMessage)(Result,error){
 var a struct{Pattern,Path,Glob string;IgnoreCase,Literal bool;Context,Limit int};if e:=parse(raw,&a);e!=nil{return failure(e)};if a.Pattern==""||a.Context<0||a.Limit<0{return failure("invalid pattern, context or limit")};if a.Limit==0{a.Limit=100};pattern:=a.Pattern;if a.Literal{pattern=regexp.QuoteMeta(pattern)};if a.IgnoreCase{pattern="(?i)"+pattern};re,e:=regexp.Compile(pattern);if e!=nil{return failure(e)};root,e:=resolve(env,a.Path);if e!=nil{return failure(e)};files,e:=candidates(ctx,env,root);if e!=nil{if ctx.Err()!=nil{return Result{},ctx.Err()};return failure(e)};var out strings.Builder;n:=0;truncated:=false
 for _,p:=range files {if e:=ctx.Err();e!=nil{return Result{},e};rel,e:=filepath.Rel(root,p);if e!=nil||a.Glob!=""&&!globMatch(a.Glob,rel){continue};text,ok:=fileText(env,p);if !ok{continue};lines:=strings.Split(text,"\n");last:=-1;for i,line:=range lines{if !re.MatchString(line){continue};if n>=a.Limit{truncated=true;break};n++;start:=i-a.Context;if start<0{start=0};if start<=last{start=last+1};end:=i+a.Context;if end>=len(lines){end=len(lines)-1};for j:=start;j<=end;j++{s:=strings.TrimSuffix(lines[j],"\r");r:=[]rune(s);if len(r)>500{s=string(r[:500])+"…"};fmt.Fprintf(&out,"%s:%d:%s\n",filepath.ToSlash(rel),j+1,s)};last=end};if truncated{break}}
 if truncated{out.WriteString("Output truncated.\n")};return Result{Text:truncate(out.String(),"Output truncated.")},nil
}
