/** The assistant's cat: a head, two eyes and the arc of a wink (the motion is in index.css, .cat-*). Decorative. */
export function CatMark({ size = 34 }: { size?: number }) {
  return (
    <svg aria-hidden className="cat flex-none" width={size} height={size} viewBox="0 0 48 48">
      <path className="cat-h" d="M24 43c-9 0-16-6-16-15 0-3.5.8-6.5 2-9L9 6l10 7c1.6-.4 3.3-.6 5-.6s3.4.2 5 .6L39 6l-1 13c1.2 2.5 2 5.5 2 9 0 9-7 15-16 15z" />
      <ellipse className="cat-l" cx="17.5" cy="27" rx="2.6" ry="3.7" />
      <ellipse className="cat-r" cx="30.5" cy="27" rx="2.6" ry="3.7" />
      <path className="cat-w" d="M27.3 28.3q3.2-3.8 6.4 0" />
    </svg>
  );
}
