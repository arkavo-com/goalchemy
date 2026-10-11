/* Maintained OpenSSL3 capability implementation; source access stays on owner. */
#include "native.h"
#include <openssl/evp.h>
#include <openssl/rand.h>
#include <openssl/pem.h>
#include <openssl/rsa.h>
#include <openssl/ec.h>
#include <openssl/ecdsa.h>
#include <openssl/x509.h>
#include <openssl/kdf.h>
#include <openssl/core_names.h>
#include <openssl/err.h>
#include <openssl/decoder.h>
#include <stdlib.h>
#include <limits.h>
#include <ctype.h>
typedef struct key_entry { uint64_t id,generation; EVP_PKEY *key; bool private_key; struct key_entry *next; } key_entry;
static EVP_PKEY_CTX *crypto_pc;
static EVP_MD_CTX *crypto_md;
static EVP_CIPHER_CTX *crypto_cipher;
static EVP_PKEY *crypto_p;
static BIO *crypto_bio;
static BIGNUM *crypto_bn1,*crypto_bn2;
static key_entry *keys;
static uint64_t next_key;
void (*gx_native_exchanges_release)(uint64_t domain);
static void keys_release_domain(uint64_t domain);
static void keys_cleanup(void) {
 EVP_PKEY_CTX_free(crypto_pc);crypto_pc=NULL;EVP_MD_CTX_free(crypto_md);crypto_md=NULL;
 EVP_CIPHER_CTX_free(crypto_cipher);crypto_cipher=NULL;EVP_PKEY_free(crypto_p);crypto_p=NULL;BIO_free(crypto_bio);crypto_bio=NULL;
 BN_free(crypto_bn1);crypto_bn1=NULL;BN_free(crypto_bn2);crypto_bn2=NULL;
 if(gx_sched->keep_native) return;
 uint64_t domain=gx_domain();if(gx_native_exchanges_release)gx_native_exchanges_release(domain);key_entry **at=&keys;
 while(*at) {key_entry *k=*at;if(k->generation!=domain){at=&k->next;continue;}*at=k->next;EVP_PKEY_free(k->key);free(k);} }
static void keys_release_domain(uint64_t domain) {
 if(gx_native_exchanges_release)gx_native_exchanges_release(domain);
 key_entry **at=&keys;
 while(*at) {key_entry *k=*at;if(k->generation!=domain){at=&k->next;continue;}*at=k->next;EVP_PKEY_free(k->key);free(k);} }
void gx_native_resources_track(void) {gx_sched->native_cleanup=keys_cleanup;gx_native_domain_release=keys_release_domain;}
static gx_V own_key(EVP_PKEY *p,bool priv) {
 if(!p) gx_host_fault("OpenSSL key acquisition");
 if(next_key==INT64_MAX) {EVP_PKEY_free(p);gx_host_fault("key identity exhausted");}
 key_entry *k=calloc(1,sizeof(*k));if(!k){EVP_PKEY_free(p);gx_host_fault("key registry allocation");}
 k->id=++next_key;k->generation=gx_domain();k->key=p;k->private_key=priv;k->next=keys;keys=k;gx_sched->native_cleanup=keys_cleanup;gx_native_domain_release=keys_release_domain;return gx_int((int64_t)k->id);
}
static key_entry *find_key(gx_V v) {
 if(v.t!=GX_INT) return NULL;
 for(key_entry *k=keys;k;k=k->next) if(k->id==(uint64_t)gx_i(v)&&k->generation==gx_domain()) return k;
 return NULL;
}
void gx_lib_crypto_close(gx_Task *t,gx_V v) {
 gx_set_rv(t,0,NULL);if(gx_is_nil(v))return;key_entry *k=find_key(v);if(!k)return;EVP_PKEY_free(k->key);k->key=NULL;
}
static bool valid_bytes(gx_V v,size_t max) {return v.t==GX_SLICE && gx_byte_backing(v)&&v.l<=max && (!v.l||v.u.p);}
static const uint8_t *crypto_bytes(gx_V v) {static const uint8_t empty=0;return gx_bytes(v)?gx_bytes(v):&empty;}
static gx_V bytes_value(const void *data,size_t n) {if(n>UINT32_MAX)gx_host_fault("native output length");uint8_t *b=gx_alloc_bytes(n?n:1);if(n)memcpy(b,data,n);return gx_byte_slice(b,(uint32_t)n,(uint32_t)n);}
static gx_V b64encode(const uint8_t *b,size_t n,bool url) {
 size_t len=4*((n+2)/3);uint8_t *out=gx_alloc_bytes(len+1);int got=EVP_EncodeBlock(out,b,(int)n);if(got<0)gx_host_fault("OpenSSL base64 encode");
 if(url){for(size_t i=0;i<len;i++){if(out[i]=='+')out[i]='-';else if(out[i]=='/')out[i]='_';}while(len&&out[len-1]=='=')len--;}
 return gx_str((const char *)out,len);
}
gx_V gx_native_encoding(gx_V v,bool url,bool decode) {
 gx_V r=decode?gx_nil_byte_slice():gx_cstr("");const char *err=NULL;
 if(!decode){if(!valid_bytes(v,GX_NATIVE_MAX))err="encoding: invalid input";else r=b64encode(gx_bytes(v),v.l,url);}
 else {
  size_t n=v.l;const uint8_t *p=gx_sbytes(v);
  if(v.t!=GX_STR || n>4*((GX_NATIVE_MAX+2)/3) || (!url&&n%4) || (url&&n%4==1))err="encoding: invalid input";
  else {
   size_t padded=(n+3)/4*4;uint8_t *text=gx_alloc_bytes(padded+1);memcpy(text,p,n);for(size_t i=n;i<padded;i++)text[i]='=';
   for(size_t i=0;i<n;i++){uint8_t c=text[i];if(url){if(c=='-')text[i]='+';else if(c=='_')text[i]='/';else if(c=='+'||c=='/'||c=='='){err="encoding: invalid input";break;}}if(!(isalnum(c)||c=='+'||c=='/'||c=='='||url&&(c=='-'||c=='_'))){err="encoding: invalid input";break;}}
   if(!err){uint8_t *data=gx_alloc_bytes(padded/4*3+1);int got=EVP_DecodeBlock(data,text,(int)padded);if(got<0)err="encoding: invalid input";else {
    size_t len=(size_t)got;if(padded&&text[padded-1]=='=')len--;if(padded>1&&text[padded-2]=='=')len--;
    gx_V canonical=b64encode(data,len,url);if(len>GX_NATIVE_MAX||canonical.l!=n||memcmp(gx_sbytes(canonical),p,n))err="encoding: invalid input";else r=bytes_value(data,len);
   }}
  }
 }
 gx_V a[2]={r,err?gx_std_errors_new(gx_cstr(err)):gx_nil()};return gx_tuple(2,a);
}
/* Coordinate width of a supported named curve, or 0; *crv receives its JWK name.
   OpenSSL names explicit parameters that resemble a named curve, so the encoding is checked too. */
static int ec_width(EVP_PKEY *p,const char **crv) {
 char group[80],encoding[32];size_t n=0;if(EVP_PKEY_base_id(p)!=EVP_PKEY_EC||EVP_PKEY_get_utf8_string_param(p,OSSL_PKEY_PARAM_GROUP_NAME,group,sizeof group,&n)<=0)return 0;
 if(EVP_PKEY_get_utf8_string_param(p,OSSL_PKEY_PARAM_EC_ENCODING,encoding,sizeof encoding,&n)<=0||strcmp(encoding,OSSL_PKEY_EC_ENCODING_GROUP))return 0;
 const char *names[]={"prime256v1","secp384r1","secp521r1"},*jwk[]={"P-256","P-384","P-521"};int widths[]={32,48,66};
 for(int i=0;i<3;i++)if(!strcmp(group,names[i])){if(crv)*crv=jwk[i];return widths[i];}
 return 0;
}
static bool valid_key(EVP_PKEY *p,bool priv) {
 if(!p)return false;int kind=EVP_PKEY_base_id(p);bool good=false;
 if(kind==EVP_PKEY_RSA){BIGNUM *n=NULL,*e=NULL;good=EVP_PKEY_get_bn_param(p,OSSL_PKEY_PARAM_RSA_N,&n)>0 && EVP_PKEY_get_bn_param(p,OSSL_PKEY_PARAM_RSA_E,&e)>0 && (BN_num_bits(n)==2048||BN_num_bits(n)==3072||BN_num_bits(n)==4096) && !BN_is_negative(n)&&BN_is_odd(e)&&BN_cmp(e,BN_value_one())>0 && BN_num_bits(e)<=31;BN_free(n);BN_free(e);}
 else if(kind==EVP_PKEY_EC)good=ec_width(p,NULL)!=0;
 if(!good)return false;EVP_PKEY_CTX *ctx=EVP_PKEY_CTX_new(p,NULL);if(!ctx)return false;
 good=EVP_PKEY_public_check(ctx)>0;if(good&&priv)good=EVP_PKEY_private_check(ctx)>0&&EVP_PKEY_pairwise_check(ctx)>0;EVP_PKEY_CTX_free(ctx);return good;
}
static EVP_PKEY *import_pem(gx_V v,bool *priv) {
 if(v.t!=GX_STR||v.l>65536)return NULL;
 const uint8_t *p=gx_sbytes(v);size_t n=v.l;while(n&&isspace(*p)){p++;n--;}while(n&&isspace(p[n-1]))n--;
 if(!n||memchr(p,0,n))return NULL;
 char *text=malloc(n+1);if(!text)gx_host_fault("PEM allocation");memcpy(text,p,n);text[n]=0;
 const char *types[]={"PUBLIC KEY","PRIVATE KEY","RSA PUBLIC KEY","RSA PRIVATE KEY","CERTIFICATE"};int type=-1;
 for(int i=0;i<5;i++){char begin[64];snprintf(begin,sizeof begin,"-----BEGIN %s-----",types[i]);size_t k=strlen(begin);if(!strncmp(text,begin,k)&&(text[k]=='\n'||text[k]=='\r'&&text[k+1]=='\n'))type=i;}
 if(type<0 || strstr(text+1,"-----BEGIN ")){free(text);return NULL;}
 BIO *bio=BIO_new_mem_buf(text,(int)n);EVP_PKEY *key=NULL;
 if(!bio){free(text);gx_host_fault("OpenSSL PEM input allocation");}
 char *pem_name=NULL,*pem_header=NULL;unsigned char *der=NULL;long derlen=0;
 bool parsed=PEM_read_bio(bio,&pem_name,&pem_header,&der,&derlen)>0 && pem_name && !strcmp(pem_name,types[type]) && pem_header && !*pem_header;
 char rest[1024];int got;bool trailing=false;while((got=BIO_read(bio,rest,sizeof rest))>0)for(int i=0;i<got;i++)if(!isspace((unsigned char)rest[i]))trailing=true;
 BIO_free(bio);free(text);
 if(parsed&&!trailing&&derlen>0){
  const unsigned char *cursor=der;size_t remaining=(size_t)derlen;
  if(type==4){X509 *cert=d2i_X509(NULL,&cursor,derlen);if(cert&&cursor==der+derlen)key=X509_get_pubkey(cert);X509_free(cert);}
  else{
   *priv=type==1||type==3;
   const char *structure=type==0?"SubjectPublicKeyInfo":type==1?"PrivateKeyInfo":"type-specific";
   OSSL_DECODER_CTX *decoder=OSSL_DECODER_CTX_new_for_pkey(&key,"DER",structure,type==2||type==3?"RSA":NULL,*priv?EVP_PKEY_KEYPAIR:EVP_PKEY_PUBLIC_KEY,NULL,NULL);
   bool decoded=decoder&&OSSL_DECODER_from_data(decoder,&cursor,&remaining)>0&&remaining==0;
   OSSL_DECODER_CTX_free(decoder);if(!decoded){EVP_PKEY_free(key);key=NULL;}
  }
 }
 OPENSSL_free(pem_name);OPENSSL_free(pem_header);OPENSSL_free(der);
 if(!valid_key(key,*priv)){EVP_PKEY_free(key);return NULL;}return key;
}
static gx_V bn_url(const BIGNUM *b,int padded) {int n=padded?padded:BN_num_bytes(b);uint8_t *bytes=gx_alloc_bytes((size_t)n);if(BN_bn2binpad(b,bytes,n)<0)gx_host_fault("OpenSSL BIGNUM encoding");return b64encode(bytes,(size_t)n,true);}
void gx_crypto_call(gx_Task *t,const char *op,int count,const gx_V *a,gx_V zero) {
 (void)count;gx_sched->native_cleanup=keys_cleanup;gx_V out=zero;const char *err=NULL,*fault=NULL;
 EVP_PKEY_CTX *pc=NULL;EVP_MD_CTX *md=NULL;EVP_CIPHER_CTX *cipher=NULL;EVP_PKEY *p=NULL;BIO *bio=NULL;
 bool success=false;key_entry *k=NULL;
 #define INVALID() do{err="crypto: invalid input or key";goto done;}while(0)
 #define REQUIRE(x) do{if(!(x))INVALID();}while(0)
 #define ALLOC(x) do{if(!(x)){fault="crypto: native allocation";goto done;}}while(0)
 #define OPENSSL(x) do{if((x)<=0){err="crypto: native operation failed";goto done;}}while(0)
 if(!strcmp(op,"random")){int64_t n=gx_i(a[0]);REQUIRE(n>=0&&n<=GX_NATIVE_MAX);uint8_t *b=gx_alloc_bytes((size_t)n);if(RAND_bytes(b,(int)n)<=0)gx_host_fault("OpenSSL randomness");out=bytes_value(b,(size_t)n);success=true;goto done;}
 if(!strcmp(op,"sha256")){REQUIRE(valid_bytes(a[0],GX_NATIVE_MAX));uint8_t b[32];unsigned n;OPENSSL(EVP_Digest(crypto_bytes(a[0]),a[0].l,b,&n,EVP_sha256(),NULL));out=bytes_value(b,n);success=true;goto done;}
 if(!strncmp(op,"hmac_",5)){REQUIRE(valid_bytes(a[0],GX_NATIVE_MAX)&&valid_bytes(a[1],GX_NATIVE_MAX));p=crypto_p=EVP_PKEY_new_raw_private_key(EVP_PKEY_HMAC,NULL,crypto_bytes(a[0]),a[0].l);ALLOC(p);md=crypto_md=EVP_MD_CTX_new();ALLOC(md);OPENSSL(EVP_DigestSignInit(md,NULL,EVP_sha256(),NULL,p));OPENSSL(EVP_DigestSignUpdate(md,crypto_bytes(a[1]),a[1].l));uint8_t b[32];size_t n=32;OPENSSL(EVP_DigestSignFinal(md,b,&n));if(strstr(op,"verify")){REQUIRE(valid_bytes(a[2],32)&&a[2].l==32);out=gx_bool(CRYPTO_memcmp(b,crypto_bytes(a[2]),32)==0);}else out=bytes_value(b,n);success=true;goto done;}
 if(!strcmp(op,"hkdf_sha256")){for(int i=0;i<3;i++)REQUIRE(valid_bytes(a[i],GX_NATIVE_MAX));int64_t n=gx_i(a[3]);REQUIRE(n>=0&&n<=8160);if(!n){out=bytes_value(NULL,0);success=true;goto done;}pc=crypto_pc=EVP_PKEY_CTX_new_id(EVP_PKEY_HKDF,NULL);ALLOC(pc);OPENSSL(EVP_PKEY_derive_init(pc));OPENSSL(EVP_PKEY_CTX_set_hkdf_md(pc,EVP_sha256()));OPENSSL(EVP_PKEY_CTX_set1_hkdf_key(pc,crypto_bytes(a[0]),a[0].l));OPENSSL(EVP_PKEY_CTX_set1_hkdf_salt(pc,crypto_bytes(a[1]),a[1].l));OPENSSL(EVP_PKEY_CTX_add1_hkdf_info(pc,crypto_bytes(a[2]),a[2].l));uint8_t *b=gx_alloc_bytes((size_t)n);size_t len=(size_t)n;if(n)OPENSSL(EVP_PKEY_derive(pc,b,&len));out=bytes_value(b,len);success=true;goto done;}
 if(!strncmp(op,"aes256_gcm_",11)){bool dec=strstr(op,"decrypt")!=NULL;REQUIRE(valid_bytes(a[0],32)&&a[0].l==32&&valid_bytes(a[1],12)&&a[1].l==12&&valid_bytes(a[2],GX_NATIVE_MAX+(dec?16:0))&&valid_bytes(a[3],GX_NATIVE_MAX));size_t n=a[2].l;if(dec){REQUIRE(n>=16);n-=16;}uint8_t *b=gx_alloc_bytes(n+16);cipher=crypto_cipher=EVP_CIPHER_CTX_new();ALLOC(cipher);OPENSSL(EVP_CipherInit_ex(cipher,EVP_aes_256_gcm(),NULL,crypto_bytes(a[0]),crypto_bytes(a[1]),!dec));int len=0,total=0;OPENSSL(EVP_CipherUpdate(cipher,NULL,&len,crypto_bytes(a[3]),(int)a[3].l));OPENSSL(EVP_CipherUpdate(cipher,b,&len,crypto_bytes(a[2]),(int)n));total=len;if(dec)OPENSSL(EVP_CIPHER_CTX_ctrl(cipher,EVP_CTRL_GCM_SET_TAG,16,crypto_bytes(a[2])+n));if(EVP_CipherFinal_ex(cipher,b+total,&len)<=0){err="crypto: authentication failed";goto done;}total+=len;if(!dec){OPENSSL(EVP_CIPHER_CTX_ctrl(cipher,EVP_CTRL_GCM_GET_TAG,16,b+total));total+=16;}out=bytes_value(b,(size_t)total);success=true;goto done;}
 if(!strncmp(op,"generate_",9)){bool rsa=strstr(op,"rsa")!=NULL;pc=crypto_pc=EVP_PKEY_CTX_new_id(rsa?EVP_PKEY_RSA:EVP_PKEY_EC,NULL);ALLOC(pc);OPENSSL(EVP_PKEY_keygen_init(pc));if(rsa)OPENSSL(EVP_PKEY_CTX_set_rsa_keygen_bits(pc,!strcmp(op,"generate_rsa4096")?4096:2048));else OPENSSL(EVP_PKEY_CTX_set_ec_paramgen_curve_nid(pc,!strcmp(op,"generate_p384")?NID_secp384r1:!strcmp(op,"generate_p521")?NID_secp521r1:NID_X9_62_prime256v1));OPENSSL(EVP_PKEY_keygen(pc,&p));crypto_p=NULL;out=own_key(p,true);p=NULL;success=true;goto done;}
 if(!strcmp(op,"import_pem")){bool priv=false;p=import_pem(a[0],&priv);REQUIRE(p);crypto_p=NULL;out=own_key(p,priv);p=NULL;success=true;goto done;}
 k=find_key(a[0]);if(!k)INVALID();if(!k->key){err="crypto: key is closed";goto done;}
 if(!strcmp(op,"public_pem")||!strcmp(op,"private_pem")){bool priv=op[1]=='r';if(priv)REQUIRE(k->private_key);bio=crypto_bio=BIO_new(BIO_s_mem());ALLOC(bio);OPENSSL(priv?PEM_write_bio_PKCS8PrivateKey(bio,k->key,NULL,NULL,0,NULL,NULL):PEM_write_bio_PUBKEY(bio,k->key));BUF_MEM *mem;BIO_get_mem_ptr(bio,&mem);out=gx_str(mem->data,mem->length);success=true;goto done;}
 if(!strcmp(op,"public_jwk")){gx_V fields[6];for(int i=0;i<6;i++)fields[i]=gx_cstr("");BIGNUM *n=NULL,*e=NULL;bool rsa=EVP_PKEY_base_id(k->key)==EVP_PKEY_RSA;if(rsa){OPENSSL(EVP_PKEY_get_bn_param(k->key,OSSL_PKEY_PARAM_RSA_N,&n));crypto_bn1=n;OPENSSL(EVP_PKEY_get_bn_param(k->key,OSSL_PKEY_PARAM_RSA_E,&e));crypto_bn2=e;fields[0]=gx_cstr("RSA");fields[2]=bn_url(n,0);fields[3]=bn_url(e,0);}else{const char *crv=NULL;int width=ec_width(k->key,&crv);REQUIRE(width);OPENSSL(EVP_PKEY_get_bn_param(k->key,OSSL_PKEY_PARAM_EC_PUB_X,&n));crypto_bn1=n;OPENSSL(EVP_PKEY_get_bn_param(k->key,OSSL_PKEY_PARAM_EC_PUB_Y,&e));crypto_bn2=e;fields[0]=gx_cstr("EC");fields[1]=gx_cstr(crv);fields[4]=bn_url(n,width);fields[5]=bn_url(e,width);}BN_free(n);crypto_bn1=NULL;BN_free(e);crypto_bn2=NULL;gx_V *v=gx_alloc_vals(6);memcpy(v,fields,sizeof fields);out=gx_slice(v,6,6);success=true;goto done;}
 if(!strcmp(op,"ecdh")){key_entry *q=find_key(a[1]);REQUIRE(k->private_key&&q&&q->key);int width=ec_width(k->key,NULL);REQUIRE(width&&width==ec_width(q->key,NULL));pc=crypto_pc=EVP_PKEY_CTX_new(k->key,NULL);ALLOC(pc);OPENSSL(EVP_PKEY_derive_init(pc));OPENSSL(EVP_PKEY_derive_set_peer(pc,q->key));uint8_t b[66];size_t n=sizeof b;OPENSSL(EVP_PKEY_derive(pc,b,&n));REQUIRE(n==(size_t)width);out=bytes_value(b,n);success=true;goto done;}
 REQUIRE(valid_bytes(a[1],GX_NATIVE_MAX));
 if(!strncmp(op,"rsa_oaep_",9)){bool dec=strstr(op,"decrypt")!=NULL;REQUIRE(EVP_PKEY_base_id(k->key)==EVP_PKEY_RSA);size_t size=(size_t)EVP_PKEY_get_size(k->key);REQUIRE((!dec||k->private_key)&&(dec?a[1].l==size:a[1].l+42<=size));pc=crypto_pc=EVP_PKEY_CTX_new(k->key,NULL);ALLOC(pc);OPENSSL(dec?EVP_PKEY_decrypt_init(pc):EVP_PKEY_encrypt_init(pc));OPENSSL(EVP_PKEY_CTX_set_rsa_padding(pc,RSA_PKCS1_OAEP_PADDING));OPENSSL(EVP_PKEY_CTX_set_rsa_oaep_md(pc,EVP_sha1()));OPENSSL(EVP_PKEY_CTX_set_rsa_mgf1_md(pc,EVP_sha1()));uint8_t b[512];size_t n=sizeof b;OPENSSL(dec?EVP_PKEY_decrypt(pc,b,&n,crypto_bytes(a[1]),a[1].l):EVP_PKEY_encrypt(pc,b,&n,crypto_bytes(a[1]),a[1].l));out=bytes_value(b,n);success=true;goto done;}
 if((op[0]=='r'||op[0]=='e')&&op[1]=='s'&&(!strncmp(op+2,"256_",4)||!strncmp(op+2,"384_",4)||!strncmp(op+2,"512_",4))){bool rsa=op[0]=='r',verify=strstr(op,"verify")!=NULL;const EVP_MD *hash=op[2]=='2'?EVP_sha256():op[2]=='3'?EVP_sha384():EVP_sha512();
  REQUIRE(EVP_PKEY_base_id(k->key)==(rsa?EVP_PKEY_RSA:EVP_PKEY_EC)&& (verify||k->private_key));int width=rsa?EVP_PKEY_get_size(k->key):ec_width(k->key,NULL);if(!rsa)REQUIRE(width==(op[2]=='2'?32:op[2]=='3'?48:66));
  md=crypto_md=EVP_MD_CTX_new();ALLOC(md);EVP_PKEY_CTX *signctx=NULL;OPENSSL(verify?EVP_DigestVerifyInit(md,&signctx,hash,NULL,k->key):EVP_DigestSignInit(md,&signctx,hash,NULL,k->key));if(rsa)OPENSSL(EVP_PKEY_CTX_set_rsa_padding(signctx,RSA_PKCS1_PADDING));
  if(verify){REQUIRE(valid_bytes(a[2],512)&&a[2].l==(size_t)(rsa?width:2*width));const uint8_t *sig=crypto_bytes(a[2]);size_t n=a[2].l;unsigned char *der=NULL;
   if(!rsa){ECDSA_SIG *s=ECDSA_SIG_new();REQUIRE(s);BIGNUM *r=BN_bin2bn(sig,width,NULL),*ss=BN_bin2bn(sig+width,width,NULL);if(!r||!ss||!ECDSA_SIG_set0(s,r,ss)){BN_free(r);BN_free(ss);ECDSA_SIG_free(s);INVALID();}int got=i2d_ECDSA_SIG(s,&der);ECDSA_SIG_free(s);REQUIRE(got>0);sig=der;n=(size_t)got;}
   out=gx_bool(EVP_DigestVerify(md,sig,n,crypto_bytes(a[1]),a[1].l)==1);OPENSSL_free(der);
  }else{uint8_t sig[512];size_t n=sizeof sig;OPENSSL(EVP_DigestSign(md,sig,&n,crypto_bytes(a[1]),a[1].l));if(rsa)out=bytes_value(sig,n);else{const unsigned char *ptr=sig;ECDSA_SIG *s=d2i_ECDSA_SIG(NULL,&ptr,(long)n);REQUIRE(s);const BIGNUM *r,*ss;ECDSA_SIG_get0(s,&r,&ss);uint8_t raw[132];bool ok=BN_bn2binpad(r,raw,width)==width&&BN_bn2binpad(ss,raw+width,width)==width;ECDSA_SIG_free(s);REQUIRE(ok);out=bytes_value(raw,(size_t)(2*width));}}
  success=true;goto done;
 }
 INVALID();
done:
 if(err&&ERR_GET_REASON(ERR_peek_last_error())==ERR_R_MALLOC_FAILURE)fault="OpenSSL allocation failure";
 EVP_PKEY_CTX_free(pc);crypto_pc=NULL;EVP_MD_CTX_free(md);crypto_md=NULL;EVP_CIPHER_CTX_free(cipher);crypto_cipher=NULL;EVP_PKEY_free(p);crypto_p=NULL;BIO_free(bio);crypto_bio=NULL;BN_free(crypto_bn1);crypto_bn1=NULL;BN_free(crypto_bn2);crypto_bn2=NULL;ERR_clear_error();
 if(fault)gx_host_fault(fault);
 gx_V result[2]={success?out:zero,err?gx_std_errors_new(gx_cstr(err)):gx_nil()};gx_set_rv(t,2,result);
 #undef INVALID
 #undef ALLOC
 #undef REQUIRE
 #undef OPENSSL
}
